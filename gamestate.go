package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// TO DO:

// Confirm that everyone in the code is consistently using zero indexing for the xy coordinates of the board
// you need to include logic to clarify if the adjacency graph and movelist are saved to player 1 or player 2
// you need to figure out how to flip the board for player 2!!!!!

type Vertex struct {
	X int `json:"x" gorm:"type:integer"`
	Y int `json:"y" gorm:"type:integer"`
}

type GameState struct {
	gorm.Model
	GameID          string   `gorm:"type:varchar(100);uniqueIndex"`
	Player1Moves    []Vertex `gorm:"type:jsonb"`
	AdjacencyGraph1 [][]int  `json:"adjacencyGraph1" gorm:"type:jsonb"`
	Player2Moves    []Vertex `gorm:"type:jsonb"`
	AdjacencyGraph2 [][]int  `json:"adjacencyGraph2" gorm:"type:jsonb"`
}

type MoveLog struct {
	ID          uint   `gorm:"primaryKey"`
	GameID      string `gorm:"type:varchar(100);index"`
	MoveCounter int
	PlayerID    string `gorm:"type:varchar(100)"`
	xCoordinate string `gorm:"type:varchar(2)"`
	yCoordinate int
	Timestamp   int64 `gorm:"autoCreateTime:milli"`
}

const SideLenGameboard = 15
const maxRetries = 3

// /.... INITIALIZE
func InitializePersistence(ctx context.Context) (*GameStatePersister, error) {
	persistLogger, ok := ctx.Value(persistLoggerKey{}).(Logger)
	if !ok {
		return nil, fmt.Errorf("error getting logger from context")
	}

	gsp, err := createGameStatePersist(ctx, persistLogger)
	if err != nil {
		persistLogger.ErrorLog(ctx, "error creating postgres persister", zap.Error(err))
		return nil, fmt.Errorf("error creating postgres persister: %w", err)
	}

	return gsp, nil
}

func InitializePostgres(ctx context.Context, persistLogger Logger) (*PostgresGameState, error) {

	//a data source name is made that tells postgresql where to connect to
	dsn := fmt.Sprintf(
		"host=db user=%s password=%s dbname=%s port=5432 sslmode=disable TimeZone=America/New_York",
		os.Getenv("DB_USER"),
		os.Getenv("DB_PASSWORD"),
		os.Getenv("DB_NAME"),
	)
	// using the dsn and the gorm config file we open a connection to the database
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: persistLogger,
	})
	if err != nil {
		persistLogger.ErrorLog(ctx, "CANNOT CONNECT TO GORM DB", zap.Error(err))
		return nil, err
	}

	// sqlDB is a lower level of abstraction but is a reference to the same underlying sql.DB struct that GORM is using, so modifying sqlDB changes the gorm postgres DB
	sqlDB, err := db.DB()
	if err != nil {
		persistLogger.ErrorLog(ctx, "error defining lower-level referece to database/sql", zap.Error(err))
		return nil, err
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(200)
	sqlDB.SetConnMaxIdleTime(time.Minute * 5)

	// using automigrate with this empty gamestateupdate creates the relational database table for us if it doesn't already exist
	db.AutoMigrate(&MoveLog{})

	// declare DB as a PostgresGameState
	return &PostgresGameState{
		DB:     db,
		logger: persistLogger,
	}, nil
}

func createGameStatePersist(ctx context.Context, persistLogger Logger) (*GameStatePersister, error) {

	redisClient := InitializeRedis(ctx)
	pgs, err := InitializePostgres(ctx)

	return &GameStatePersister{
		postgres: pgs,
		redis: &RedisGameState{
			redisClient,
			persistLogger,
		},
	}, err
}

// ..... HANDLER

func (gsp *GameStatePersister) HandleGameStateUpdate(newMove *GameStateUpdate) (interface{}, bool) {
	pg := gsp.postgres
	rs := gsp.redis

	ctx, cancelFunc := context.WithCancel(context.Background())
	playerID := newMove.PlayerID
	moveCounter := newMove.MoveCounter
	gameID := newMove.GameID
	xCoord := newMove.xCoordinate
	yCoord := newMove.yCoordinate

	newVert, err := convertToTypeVertex(xCoord, yCoord)
	if err != nil {
		//handle
	}
	// retrieve adjacency matrix and movelist from redis, with postgres as fall back option
	oldAdjG, oldMoveList := rs.FetchGameState(ctx, newVert, gameID, playerID, moveCounter)
	newAdjG, newMoveList := IncorporateNewVert(ctx, oldMoveList, oldAdjG, newVert)

	win_yn := evalWinCondition(newAdjG, newMoveList)

	err = pg.UpdateGS_sql(ctx, newMove)
	if err != nil {
		// Handle the error
	}

	if err := rs.PersistGraphToRedis(ctx, newAdjG, gameID, playerID); err != nil {
		RetryFunc(func() error { return rs.PersistGraphToRedis(ctx, newAdjG, gameID, playerID) })
	}
	if err := rs.PersistMoveToRedisList(ctx, newVert, gameID, playerID); err != nil {
		RetryFunc(func() error { return rs.PersistMoveToRedisList(ctx, newVert, gameID, playerID) })
	}

	// cancel the context for this event handling
	cancelFunc()

	loserID := lg.Player1.PlayerID
	lg := allLockedGames[gameID] //check all lockedGames map
	if lg.Player1.PlayerID == playerID {
		loserID = lg.Player2.PlayerID
	}

	if win_yn {
		newEvt := &EndingGameCmd{
			GameID:       gameID,
			WinnerID:     playerID,
			LoserID:      loserID,
			WinCondition: "A True Win",
		}
		return newEvt, false

	} else {
		nextPlayer := "P2"
		if playerID != "P1" {
			nextPlayer = "P1"
		}

		newEvt := &NextTurnStartsEvent{
			GameID:   gameID,
			PriorPlayerID: playerID,
			NextPlayerID: nextPlayer,
			UpcomingMoveNumber: moveCounter + 1,
			TimeStamp: 	time.Now(),
		}
		return newEvt, true
	}

	

	// include a check to compare redis and postgres?
}

func RetryFunc(function func() error) error {
	var err error
	for i := 0; i < maxRetries; i++ {
		err = function()
		if err == nil {
			return nil
		}
	}
	return err
}

func (rs *RedisGameState) PersistGraphToRedis(ctx context.Context, graph [][]int, gameID, playerID string) error {
	var serialized strings.Builder
	for _, row := range graph {
		for _, cell := range row {
			serialized.WriteString(fmt.Sprintf("%d,", cell))
		}
		serialized.WriteString(";")
	}

	key := fmt.Sprintf("adjGraph:%s:%s", gameID, playerID)
	serializedGraph := serialized.String()
	_, err := rs.Set(ctx, key, serializedGraph, 0).Result()
	if err != nil {
		return err
	}

	return nil
}

func (rs *RedisGameState) PersistMoveToRedisList(ctx context.Context, newMove Vertex, gameID, playerID string) error {
	moveKey := fmt.Sprintf("moveList:%s:%s", gameID, playerID)
	serializedMove := fmt.Sprintf("%d,%d", newMove.X, newMove.Y)
	_, err := rs.client.RPush(ctx, moveKey, serializedMove).Result()
	if err != nil {
		return err
	}

	return nil
}

// ..... FETCHING FROM REDIS
func (gsp *GameStatePersister) FetchGameState(ctx context.Context, newVert Vertex, gameID, playerID string, moveCounter int) ([][]int, []Vertex) {

	pg := gsp.postgres
	rs := gsp.redis

	adjGraph, err := rs.FetchAdjacencyGraph(ctx, gameID, playerID)
	moveList, err2 := rs.FetchPlayerMoves(ctx, gameID, playerID)

	if err != nil || err2 != nil {
		make([]slice, 
		adjGraph, moveList, _, err = pg.FetchGS_sql(ctx, gameID, playerID, moveCounter)
		if err != nil {
			panic(err)
		}
	}

	return adjGraph, moveList
}

func (rs *RedisGameState) FetchPlayerMoves(ctx context.Context, gameID, playerID string) ([]Vertex, error) {
	// Construct the key for fetching the list of moves.
	movesKey := fmt.Sprintf("%s:%s:Moves", gameID, playerID)

	emptyVal := []Vertex{}

	// Fetch all moves from Redis using LRange.
	movesJSON, err := rs.client.LRange(ctx, movesKey, 0, -1).Result()
	if err != nil {
		// Handle the error.
		return emptyVal, err
	}

	// Decode each move from its JSON representation.
	var moves []Vertex
	for _, moveStr := range movesJSON {
		var move Vertex
		err = json.Unmarshal([]byte(moveStr), &move)
		if err != nil {
			// Handle the error. Depending on your application, you might want to continue to the next move or exit early.
			return emptyVal, err
		}
		moves = append(moves, move)
	}

	return moves, nil
}

func (rs *RedisGameState) FetchAdjacencyGraph(ctx context.Context, gameID, playerID string) ([][]int, error) {
	// Construct the key for fetching the adjacency graph.
	adjacencyGraphKey := fmt.Sprintf("%s:%s:AdjacencyGraph", gameID, playerID)

	emptyVal := [][]int{{0, 0}, {0, 0}}

	// Fetch the adjacency graph from Redis.
	adjacencyGraphJSON, err := rs.client.Get(ctx, adjacencyGraphKey).Result()
	if err != nil {
		// Handle the error. It's common for the error to be `redis.Nil` if the key doesn't exist.
		return emptyVal, err
	}

	// Decode the adjacency graph from its JSON representation.
	var adjacencyGraph [][]int
	err = json.Unmarshal([]byte(adjacencyGraphJSON), &adjacencyGraph)
	if err != nil {
		return emptyVal, err
	}

	return adjacencyGraph, nil
}

// FETCH FROM POSTGRESQL

func (pg *PostgresGameState) FetchGS_sql(ctx context.Context, gameID, playerID string) ([]string, []int, int, error) {
	var moveLogs []MoveLog

	if playerID == "all" { // this is called by the DeclaringMoveCommand handler
		result := pg.DB.Where("game_id = ?", gameID).Order("move_counter asc").Find(&moveLogs)
		if result.Error != nil {
			return nil, nil, 0, result.Error
		}
	} else {
		result := pg.DB.Where("game_id = ? AND player_id = ?", gameID, playerID).Order("move_counter asc").Find(&moveLogs)
		if result.Error != nil {
			return nil, nil, 0, result.Error
		}
	}

	numMoves := len(moveLogs)
	moveCounter := numMoves * 2
	if playerID == "P1" {
		moveCounter = moveCounter - 1
	} else if playerID == "all" {
		moveCounter = numMoves
	}

	xCoords := make([]string, numMoves)
	yCoords := make([]int, numMoves)

	for i, move := range moveLogs {
		xCoords[i] = move.xCoordinate
		yCoords[i] = move.yCoordinate
	}

	return xCoords, yCoords, moveCounter, nil
}

// ..... UPDATING POSTGRESQL

func (pg *PostgresGameState) UpdateGS_sql(ctx context.Context, update *GameStateUpdate) error {

	txOptions := &sql.TxOptions{
		Isolation: sql.LevelSerializable,
	}
	tx := pg.DB.WithContext(ctx).Begin(txOptions)
	if tx.Error != nil {
		return tx.Error
	}

	moveLog := MoveLog{
		GameID:      update.GameID,
		PlayerID:    update.PlayerID,
		MoveCounter: update.MoveCounter,
		xCoordinate: update.xCoordinate,
		yCoordinate: update.yCoordinate,
	}

	// Save the move to the MoveLog table.
	if err := tx.Create(&moveLog).Error; err != nil {
		tx.Rollback()
		return err
	}

	if err := tx.Commit().Error; err != nil {
		return err
	}

	return nil
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

		return nil, nil, fmt.Errorf("Found more moves than anticipated")
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

	// Incorporate the moves
	for i := 0; i < len(xCoords); i++ {
		vert, err := convertToTypeVertex(xCoords[i], yCoords[i])
		if err != nil {
			return nil, nil, err
		}

		adjGraph, moveList = IncorporateNewVert(ctx, moveList, adjGraph, vert)
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
		//If we make it to here then we have not thinned enough, and so we go forward with the next iteration of thinning
	}
}

// ..... Helper functions

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
		return nil, fmt.Errorf("gamestate breakdown", "Adjacency matrix is not symmetric, something went wrong, terribly wrong")
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

func convertToTypeVertex(xCoord string, yCoord int) (Vertex, error) {
	x, err := convertToInt(xCoord)
	if err != nil {
		return Vertex{}, err
	}

	return Vertex{X: x, Y: yCoord}, nil
}

// vv maybe I dont need these any more!!!

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
