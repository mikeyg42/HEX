package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	hex "github.com/mikeyg42/HEX/models"
	"github.com/mikeyg42/HEX/retry"
	storage "github.com/mikeyg42/HEX/storage"
	ti "github.com/mikeyg42/HEX/timerpkg"
	zap "go.uber.org/zap"
	gormlogger "gorm.io/gorm/logger"

)

const SideLenGameboard = 15
const maxRetries = 3

type GameState struct {
	Persister *hex.GameStatePersister
	Errlong   *storage.Logger
	GameID    string
	Timer     ti.TimerControl
}

func (gs *GameState) HandleGameStateUpdate(newMove *hex.GameStateUpdate) interface{} {
	pg := gs.Persister.Postgres
	rs := gs.Persister.Redis

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	// Assuming you have a zap logger instance called `logger`
	logger := storage.InitLogger("path/to/logs/gamestate.log", gormlogger.LogLevel(0))
	ctx = context.WithValue(ctx, "logger", logger)

	playerID := newMove.PlayerID

	moveCounter := newMove.MoveCounter
	gameID := newMove.GameID
	xCoord := newMove.XCoordinate
	yCoord := newMove.YCoordinate

	newVert, err := convertToTypeVertex(xCoord, yCoord)
	if err != nil {
		//handle
	}

	// retrieve adjacency matrix and movelist from redis, with postgres as fall back option
	oldAdjG, oldMoveList := storage.FetchGameState(ctx, newVert, gameID, playerID, moveCounter, gs.Persister)
	newAdjG, newMoveList := IncorporateNewVert(ctx, oldMoveList, oldAdjG, newVert)

	// DONE UPDATING GAMESTATE! NOW WE USE IT
	win_yn := evalWinCondition(newAdjG, newMoveList)

	// ... AND SAVE IT
	err = storage.PersistGameState_sql(ctx, newMove, pg)
	if err != nil {
		// Handle the error
	}

	var wg sync.WaitGroup
	wg.Add(2) // two persistence actions

	go func() {
		defer wg.Done()
		r := retry.RetryFunc(ctx, func() (string, interface{}, error) {
			err := storage.PersistGraphToRedis(ctx, newAdjG, gameID, playerID, rs)
			return "", nil, err
		})
		if r.Err != nil {
			// Handle the error, possibly by logging it
			logger.Error(ctx, "Failed to persist graph to Redis: %v", zap.Error(r.Err))
		}
	}()

	go func() {
		defer wg.Done()
		r := retry.RetryFunc(ctx, func() (string, interface{}, error) {
			err := storage.PersistMoveToRedisList(ctx, newVert, gameID, playerID, rs)
			return "", nil, err
		})
		if r.Err != nil {
			// Handle the error, possibly by logging it
			logger.Error(ctx, "Failed to persist move List to Redis: %v", zap.Error(r.Err))
		}
	}()

	wg.Wait() // Wait for both persistence actions to complete

	// cancel the context for this event handling
	cancelFunc()

	if win_yn {
		// use the lockedGames map to ascertain who opponent is (ie who's turn is next )
		lg := allLockedGames[gameID] //check all lockedGames map
		loserID := lg.Player1.PlayerID
		if lg.Player1.PlayerID == playerID {
			loserID = lg.Player2.PlayerID
		}

		newEvt := &hex.GameEndEvent{
			GameID:       gameID,
			WinnerID:     playerID,
			LoserID:      loserID,
			WinCondition: "A True Win",
		}
		return newEvt

	} else {
		nextPlayer := "P2"
		if playerID != "P1" {
			nextPlayer = "P1"
		}

		newCmd := &hex.NextTurnStartingCmd{
			GameID:             gameID,
			PriorPlayerID:      playerID,
			NextPlayerID:       nextPlayer,
			UpcomingMoveNumber: moveCounter + 1,
			TimeStamp:          time.Now(),
		}
		return newCmd
	}

	// include a check to compare redis and postgres?
}

type RResult struct {
	Err     error
	Message string
}

type FuncWithErrorOnly func() error
type FuncWithResultAndError func() *RResult

func (gs *GameState) RetryFunc(ctx context.Context, function interface{}) *RResult {
	var msg string = ""
	var lastErr error = nil

	for i := 0; i < maxRetries; i++ {
	
		switch f := function.(type) {
		case FuncWithErrorOnly:
			lastErr = f()

		case FuncWithResultAndError:
			result := f()
			lastErr = result.Err
			msg = result.Message

			if lastErr == nil {
				gs.Errlong.InfoLog(ctx, "RetryFunc SUCCESS", zap.Int("RetryFunc iteration num", i))
				return &RResult{
					Err:     lastErr,
					Message: msg,
				}
			}
		}

		gs.Errlong.InfoLog(ctx, fmt.Sprintf("Attempt %d failed with error: %v. Retrying...\n", i+1, lastErr), zap.Int("RetryFunc iteration num", i))
		// Introduce a delay with exponential backoff

		time.Sleep(time.Millisecond * 100 * time.Duration(1<<i))

	}
	err := fmt.Errorf("failed after %d attempts. Last error: %v", maxRetries, lastErr)

	return &RResult{
		Err:     err,
		Message: msg,
	}
}

func (gs *GameState) RecreateGS_Postgres(ctx context.Context, playerID, gameID string, moveCounter int) ([][]int, []hex.Vertex, error) {

	// this function assumes that you have NOT yet incorporated the newest move into this postgres. but moveCounter input is that of the new move yet to be added
	adjGraph := [][]int{{0, 0}, {0, 0}}
	moveList := []hex.Vertex{}

	// Fetch moves that match the gameID and the playerID and with a moveCounter less than the passed moveCounter.
	xCoords, yCoords, moveCounterCheck, err := storage.FetchGS_sql(ctx, gameID, playerID, gs.Persister.Postgres)
	if err != nil {
		return nil, nil, err
	}

	// Return error if we have moves greater than or equal to moveCounter.
	if len(moveCounterCheck) > 1 || moveCounterCheck[0] >= moveCounter {
		// CHECK FOR DUPLICATE ENTRIES?
		return nil, nil, fmt.Errorf("found more moves than anticipated")
	}

	if moveCounter == 1 || moveCounter == 2 {
		switch len(xCoords) {
		case 0:
			return adjGraph, moveList, nil
		case 1:
			vert, err := convertToTypeVertex(xCoords[0], yCoords[0])
			if err != nil {
				return nil, nil, err
			}
			moveList = append(moveList, vert)
			adjGraph = [][]int{{0, 0, 0}, {0, 0, 0}, {0, 0, 0}}
			return adjGraph, moveList, nil
		default:
			return nil, nil, fmt.Errorf("unexpected number of moves")
		}
	}

	for i := 0; i < len(xCoords); i++ {
		modVert, err := convertToTypeVertex(xCoords[i], yCoords[i])
		if err != nil {
			return nil, nil, err
		}

		adjGraph, moveList = IncorporateNewVert(ctx, moveList, adjGraph, modVert)
	}

	return adjGraph, moveList, nil
}

// ..... CHECKING FOR WIN CONDITION

func evalWinCondition(adjG [][]int, moveList []hex.Vertex) bool {
	numMoves := len(moveList)
	numRows := numMoves + 2
	numCols := numRows

	// Condition 1: Check if enough tiles to traverse the whole game board
	if numMoves < SideLenGameboard {
		return false
	}

	// Condition 2: Check if at least 1 tile placed in each column
	for i := 0; i < SideLenGameboard; i++ {
		colExists := false
		for _, move := range moveList {
			if move.X == i {
				colExists = true
				break
			}
		}
		if !colExists {
			return false
		}
	}

	thinnedAdj := make([][]int, len(adjG))
	copy(thinnedAdj, adjG)
	thinnedMoveList := make([]hex.Vertex, numMoves)
	copy(thinnedMoveList, moveList)

	for {
		// Find degree 0 and 1 nodes (excluding end points)
		lowDegreeNodes := make([]int, 0)
		for i := 2; i < numRows; i++ {
			degree := 0
			for j := 0; j < numCols; j++ {
				degree += thinnedAdj[i][j]
			}
			if degree == 0 || degree == 1 {
				lowDegreeNodes = append(lowDegreeNodes, i)
			}
		}

		// If there are no degree 0 or 1 nodes, break the loop
		if len(lowDegreeNodes) == 0 {
			return true
		}

		var err error
		thinnedAdj, err = thinAdjacencyMat(thinnedAdj, lowDegreeNodes)
		if err != nil {
			panic(err)
		}

		// Update adjacency matrix and dimensions
		numRows = len(thinnedAdj)
		numCols = numRows

		// Update move list
		thinnedMoveList = removeVertices(thinnedMoveList, lowDegreeNodes)

		// Check if condition 1 and 2 are still met
		if len(thinnedMoveList) < SideLenGameboard {
			return false
		}
		columnSet := make(map[int]bool)
		for _, move := range thinnedMoveList {
			columnSet[move.X] = true
		}
		//... and then here, we iterate over width of gameboard, and any columns not represented in the colunnSet map we change to false
		for i := 0; i < SideLenGameboard; i++ {
			if !columnSet[i] {
				return false
			}
		}
		//If we make it to here then we have not thinned enough, and so we proceed with another iteration of thinning
	}
}

//............................. //
// ..... Helper functions ..... //

func IncorporateNewVert(ctx context.Context, moveList []hex.Vertex, adjGraph [][]int, newVert hex.Vertex) ([][]int, []hex.Vertex) {
	// Update Moves
	updatedMoveList := append(moveList, newVert)
	moveCount := len(updatedMoveList)

	// Calculate new size having now appended a new vertex
	sizeNewAdj := moveCount + 2

	// Preallocate/new adjacency graph with initial values copied from the old adjacency graph
	newAdjacencyGraph := make([][]int, sizeNewAdj)
	for i := 0; i < sizeNewAdj; i++ {
		newAdjacencyGraph[i] = make([]int, sizeNewAdj)
	}

	// Copy values from the old adjacency graph
	for i := 0; i < len(adjGraph); i++ {
		copy(newAdjacencyGraph[i], adjGraph[i])
	}

	// Find adjacent vertices by comparing the list of 6 adjacent vertex list to game state move list, if one of the new points neighbors is in the move list, then there is an edge between the new point and that existing point
	sixAdjacentVertices := getAdjacentVertices(newVert)
	for i := 0; i < moveCount; i++ {
		k := i + 2
		for _, potentialEdgePair := range sixAdjacentVertices {
			if containsVert(moveList, potentialEdgePair) {
				// Edge found between new vertex and an existing vertex
				newAdjacencyGraph[k][moveCount] = 1
				newAdjacencyGraph[moveCount][k] = 1
				break // is this right?
			}
		}
	}

	// Check if new vertex is in the first column or last column
	if newVert.X == 0 {
		// Edge found between new vertex and the leftmost hidden point
		newAdjacencyGraph[0][sizeNewAdj-1] = 1
		newAdjacencyGraph[sizeNewAdj-1][0] = 1
	} else if newVert.X == SideLenGameboard-1 {
		// Edge found between new vertex and rightmost hidden point
		newAdjacencyGraph[1][sizeNewAdj-1] = 1
		newAdjacencyGraph[sizeNewAdj-1][1] = 1
	}

	return newAdjacencyGraph, updatedMoveList
}

func getAdjacentVertices(vertex hex.Vertex) []hex.Vertex {
	return []hex.Vertex{
		{X: vertex.X - 1, Y: vertex.Y + 1},
		{X: vertex.X - 1, Y: vertex.Y},
		{X: vertex.X, Y: vertex.Y - 1},
		{X: vertex.X, Y: vertex.Y + 1},
		{X: vertex.X + 1, Y: vertex.Y},
		{X: vertex.X + 1, Y: vertex.Y - 1},
	}
}

func thinAdjacencyMat(adj [][]int, indices []int) ([][]int, error) {
	temp := removeRows(adj, indices)
	temp = transpose(temp)
	thinnedAdj := removeRows(temp, indices)

	// Check for matrix symmetry
	if isSymmetric(thinnedAdj) {
		return nil, fmt.Errorf("gamestate breakdown: %v", "Adjacency matrix is not symmetric, something went wrong, terribly wrong")
	}

	return thinnedAdj, nil
}

func containsInt(items []int, item int) bool {
	for _, val := range items {
		if val == item {
			return true
		}
	}
	return false
}

func containsVert(vertices []hex.Vertex, target hex.Vertex) bool {
	for _, v := range vertices {
		if v.X == target.X && v.Y == target.Y {
			return true
		}
	}
	return false
}

func removeRows(s [][]int, indices []int) [][]int {
	result := make([][]int, 0)
	for i, row := range s {
		if containsInt(indices, i) {
			continue
		}
		newRow := make([]int, 0)
		for j, val := range row {
			if containsInt(indices, j) {
				continue
			}
			newRow = append(newRow, val)
		}
		result = append(result, newRow)
	}
	return result
}

func removeVertices(s []hex.Vertex, indices []int) []hex.Vertex {
	result := make([]hex.Vertex, 0)
	for i, vertex := range s {
		if containsInt(indices, i) {
			continue
		}
		result = append(result, vertex)
	}
	return result
}

func isSymmetric(matrix [][]int) bool {
	rows := len(matrix)
	if rows == 0 {
		return true
	}
	cols := len(matrix[0])

	for i, row := range matrix {
		if len(row) != cols {
			return false
		}
		for j := i + 1; j < cols; j++ {
			if matrix[i][j] != matrix[j][i] {
				return false
			}
		}
	}

	return true
}

func transpose(slice [][]int) [][]int {
	numRows := len(slice)
	if numRows == 0 {
		return [][]int{}
	}
	numCols := len(slice[0])
	result := make([][]int, numCols)
	for i := range result {
		result[i] = make([]int, numRows)
	}
	for i, row := range slice {
		for j, val := range row {
			result[j][i] = val
		}
	}
	return result
}

func convertToInt(xCoord string) (int, error) {
	// x coordinate will always be a letter between A and O, the 15th letter, and bc the board is 15x15, the max value for xCoord is 15
	if len(xCoord) != 1 {
		return -1, fmt.Errorf("invalid input length for xCoord")
	}
	// coordinates on our game board are zero-indexed! So we want a--> 0, b--> 1, etc. until o --> 14
	return int(xCoord[0]) - int('A'), nil
}

func convertToTypeVertex(xCoord string, yCoord int) (hex.Vertex, error) {
	x, err := convertToInt(xCoord)
	if err != nil {
		return hex.Vertex{}, err
	}
	return hex.Vertex{X: x, Y: yCoord}, nil
}

func largestKey(m []string) int {
	maxKey := 0
	for k := range m {
		if k > maxKey {
			maxKey = k
		}
	}
	return maxKey
}

func checkDatastoreSize(data []string) error {
	// here we calculate the  size using length and then we also identify the highest move #. if nothing is skipped that, for ex. in a 3 item list starting at 1, the highest value will be also 3

	numElements := len(data)
	largestKey := largestKey(data)

	diff := largestKey - int(numElements)

	if diff != 0 {
		err := fmt.Errorf("error when querying datastore", zap.String("missing the following # of elements in data map: ", string(diff)))
		return err
	}

	return nil
}
