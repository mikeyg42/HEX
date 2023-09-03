package main

import (
	"context"
	"fmt"
	"time"
    _ "github.com/mikeyg42/HEX/models"

	zap "go.uber.org/zap"
	"gorm.io/gorm"
)

const SideLenGameboard = 15
const maxRetries = 3

// ..... HANDLERS ............//

func (con *GameContainer) HandleGameStateUpdate(newMove *GameStateUpdate) interface{} {
	pg := con.Persister.postgres
	rs := con.Persister.redis

	ctx, cancelFunc := context.WithCancel(context.Background())

	playerID := newMove.PlayerID

	moveCounter := newMove.MoveCounter
	gameID := newMove.GameID
	xCoord := newMove.xCoordinate
	yCoord := newMove.yCoordinate

	// FIX THIS!!
	yn_rotateboard := true
	if moveCounter%2 == 0 {
		yn_rotateboard = false
	}

	newVert, err := convertToTypeVertex(xCoord, yCoord, yn_rotateboard)
	if err != nil {
		//handle
	}

	// retrieve adjacency matrix and movelist from redis, with postgres as fall back option
	oldAdjG, oldMoveList := con.Persister.FetchGameState(ctx, newVert, gameID, playerID, moveCounter)
	newAdjG, newMoveList := IncorporateNewVert(ctx, oldMoveList, oldAdjG, newVert)

	// DONE UPDATING GAMESTATE! NOW WE USE IT
	win_yn := evalWinCondition(newAdjG, newMoveList)
	
	// ... AND SAVE IT
	err = pg.PersistGameState_sql(ctx, newMove)
	
	if err != nil {
		// Handle the error
	}
	// adjacency matrix
	if err := rs.PersistGraphToRedis(ctx, newAdjG, gameID, playerID); err != nil {
		con.RetryFunc(ctx,func() error { return rs.PersistGraphToRedis(ctx, newAdjG, gameID, playerID) })
	}
	// move List
	if err := rs.PersistMoveToRedisList(ctx, newVert, gameID, playerID); err != nil {
		con.RetryFunc(ctx,func() error { return rs.PersistMoveToRedisList(ctx, newVert, gameID, playerID) })
	}

	// cancel the context for this event handling
	cancelFunc()

	if win_yn {
		// use the lockedGames map to ascertain who opponent is (ie who's turn is next )
		lg := allLockedGames[gameID] //check all lockedGames map
		loserID := lg.Player1.PlayerID
		if lg.Player1.PlayerID == playerID {
			loserID = lg.Player2.PlayerID
		}

		newCmd := &hex.EndingGameCmd{
			GameID:       gameID,
			WinnerID:     playerID,
			LoserID:      loserID,
			WinCondition: "A True Win",
		}
		return newCmd

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
type FuncWithResult func() *RResult

func (con *GameContainer) RetryFunc(ctx context.Context, function interface{}) *RResult {
	var lastErr error
	var msg string

	for i := 0; i < maxRetries; i++ {
		switch f := function.(type) {
		case FuncWithErrorOnly:
			lastErr = f()
		case FuncWithResult:
			result := f()
			lastErr = result.Err
			msg = result.Message
		default:
			return &RResult{Err: fmt.Errorf("unsupported function type")}
		}

		if lastErr == nil {
			con.ErrorLog.InfoLog(ctx, "RetryFunc SUCCESS", zap.Int("RetryFunc iteration num", i))
			return &RResult{
				Err:     nil,
				Message: msg,
			}
		}

		con.ErrorLog.InfoLog(ctx, fmt.Sprintf("Attempt %d failed with error: %v. Retrying...\n", i+1, lastErr), zap.Int("RetryFunc iteration num", i))
		// Introduce a delay with exponential backoff
		time.Sleep(time.Second * time.Duration(1<<i))
	}

	err := fmt.Errorf("failed after %d attempts. Last error: %v", maxRetries, lastErr)

	return &RResult{
		Err:     err,
		Message: msg,
	}
}

func (pg *PostgresGameState) RecreateGS_Postgres(ctx context.Context, playerID, gameID string, moveCounter int) ([][]int, []Vertex, error) {
	// this function assumes that you have NOT yet incorporated the newest move into this postgres. but moveCounter input is that of the new move yet to be added
	adjGraph := [][]int{{0, 0}, {0, 0}}
	moveList := []Vertex{}

	// Fetch moves that match the gameID and the playerID and with a moveCounter less than the passed moveCounter.
	xCoords, yCoords, moveCounterCheck, err := pg.FetchGS_sql(ctx, gameID, playerID)
	if err != nil {
		return nil, nil, err
	}

	// Return error if we have moves greater than or equal to moveCounter.
	if moveCounterCheck >= moveCounter {
		// CHECK FOR DUPLICATE ENTRIES?

		return nil, nil, fmt.Errorf("found more moves than anticipated")
	}

	if moveCounter == 1 || moveCounter == 2 {
		switch len(xCoords) {
		case 0:
			return adjGraph, moveList, nil
		case 1:
			vert, err := convertToTypeVertex(xCoords[0], yCoords[0], false)
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

	// Incorporate the moves
	yn_rotateboard := true
	if moveCounter%2 == 0 {
		yn_rotateboard = false
	}

	for i := 0; i < len(xCoords); i++ {
		modVert, err := convertToTypeVertex(xCoords[i], yCoords[i], yn_rotateboard)
		if err != nil {
			return nil, nil, err
		}

		adjGraph, moveList = IncorporateNewVert(ctx, moveList, adjGraph, modVert)
	}

	return adjGraph, moveList, nil
}

// ..... CHECKING FOR WIN CONDITION

func evalWinCondition(adjG [][]int, moveList []Vertex) bool {
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
	thinnedMoveList := make([]Vertex, numMoves)
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

// ..... Helper functions ..... //

func IncorporateNewVert(ctx context.Context, moveList []Vertex, adjGraph [][]int, newVert Vertex) ([][]int, []Vertex) {
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

func getAdjacentVertices(vertex Vertex) []Vertex {
	return []Vertex{
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

func containsVert(vertices []Vertex, target Vertex) bool {
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

func removeVertices(s []Vertex, indices []int) []Vertex {
	result := make([]Vertex, 0)
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

func convertToTypeVertex(xCoord string, yCoord int, yes_rotate bool) (Vertex, error) {
	x, err := convertToInt(xCoord)
	if err != nil {
		return Vertex{}, err
	}
	if yes_rotate {
		return Vertex{X: yCoord, Y: x}, nil
	} else {
		return Vertex{X: x, Y: yCoord}, nil
	}
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
