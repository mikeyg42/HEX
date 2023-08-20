package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	redis "github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// .... INITIALIZE
func InitializePersistence(ctx context.Context) (*GameStatePersister, error, error, error) {
	persistLogger, ok := ctx.Value(persistLoggerKey{}).(Logger)
	if !ok {
		return nil, fmt.Errorf("error getting logger from context"), nil, nil
	}

	// create connection to postgres, and set up timeout functionality
	pgs, pgerr := InitializePostgres(persistLogger)
	if pgerr != nil {
		return nil, nil, fmt.Errorf("InitError - Postgres"), nil
	}

	// create connection to redis, and set up context and timeout functionality differently
	rgs, rgerr := InitializeRedis(persistLogger)
	if rgerr != nil {
		return nil, nil, nil, fmt.Errorf("InitError - Redis")
	}

	gsp := &GameStatePersister{
		postgres: pgs,
		redis:    rgs,
	}

	return gsp, nil, nil, nil
}

func InitializeRedis(persistLogger Logger) (*RedisGameState, error) {
	// make a connection to redis. we only need one of these per server
	redisClient, err := getRedisClient(RedisAddr) // this address is at the moment a global variable
	ctx := context.Background()
	rgs := &RedisGameState{
		Client:     redisClient,     // client
		Logger:     persistLogger,   // logger
		Context:    ctx,             // ctx
		TimeoutDur: 5 * time.Second, // duration
	}
	return rgs, err
}

func InitializePostgres(persistLogger Logger) (*PostgresGameState, error) {

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
		return nil, err
	}

	// sqlDB is a lower level of abstraction but is a reference to the same underlying sql.DB struct that GORM is using, so modifying sqlDB changes the gorm postgres DB
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(200)
	sqlDB.SetConnMaxIdleTime(time.Minute * 5)

	// using automigrate with this empty gamestateupdate creates the relational database table for us if it doesn't already exist
	db.AutoMigrate(&MoveLog{})

	// declare DB as a PostgresGameState
	return &PostgresGameState{
		DB:         db,
		Logger:     persistLogger,
		Context:    context.Background(), //nrw one
		TimeoutDur: time.Second * 10,
	}, nil
}

// PERSIST functions

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
	_, err := rs.Client.Set(ctx, key, serializedGraph, 0).Result()
	if err != nil {
		return err
	}

	return nil
}

func (rs *RedisGameState) PersistMoveToRedisList(ctx context.Context, newMove Vertex, gameID, playerID string) error {
	moveKey := fmt.Sprintf("moveList:%s:%s", gameID, playerID)
	serializedMove := fmt.Sprintf("%d,%d", newMove.X, newMove.Y)
	_, err := rs.Client.RPush(ctx, moveKey, serializedMove).Result()
	if err != nil {
		return err
	}

	return nil
}

func (pg *PostgresGameState) PersistGameState_sql(ctx context.Context, update *GameStateUpdate) error {

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

// ..... FETCHING FROM REDIS
func (gsp *GameStatePersister) FetchGameState(ctx context.Context, newVert Vertex, gameID, playerID string, moveCounter int) ([][]int, []Vertex) {

	pg := gsp.postgres
	rs := gsp.redis

	adjGraph, err := rs.FetchAdjacencyGraph(ctx, gameID, playerID)
	moveList, err2 := rs.FetchPlayerMoves(ctx, gameID, playerID)

	if err != nil || err2 != nil {
		err := fmt.Errorf("Redis persistence failed, falling back to Postgres.")
		adjGraph, moveList, err = pg.RecreateGS_Postgres(ctx, playerID, gameID, moveCounter)
		if err != nil {
			panic(err)
		}
	}

	return adjGraph, moveList
}

func (rs *RedisGameState) FetchPlayerMoves(ctx context.Context, gameID, playerID string) ([]Vertex, error) {
	// Construct the key for fetching the list of moves.
	movesKey := fmt.Sprintf("MoveList:%s:%s", gameID, playerID)

	emptyVal := []Vertex{}
	ctx_timeout, cancel := context.WithTimeout(rs.Context, rs.TimeoutDur) // for example, 1 minute timeout
	defer cancel()

	// Fetch all moves from Redis using LRange.
	movesJSON, err := rs.Client.LRange(ctx_timeout, movesKey, 0, -1).Result()
	if err != nil {
		// log the error. maybe retry?
		return emptyVal, err
	}

	// Decode each move from its JSON representation.
	var moves []Vertex
	for _, moveStr := range movesJSON {
		var move Vertex
		err = json.Unmarshal([]byte(moveStr), &move)
		if err != nil {
			// Handle unmarshalling error somehow?
			return emptyVal, err
		}
		moves = append(moves, move)
	}

	return moves, nil
}

func (rs *RedisGameState) FetchAdjacencyGraph(ctx context.Context, gameID, playerID string) ([][]int, error) {
	// Construct the key for fetching the adjacency graph.
	adjacencyGraphKey := fmt.Sprintf("AdjGraph:%s:%s", gameID, playerID)

	emptyVal := [][]int{{0, 0}, {0, 0}}

	// Fetch the adjacency graph from Redis.
	ctx_timeout, cancel := context.WithTimeout(rs.Context, rs.TimeoutDur)
	defer cancel()

	adjacencyGraphJSON, err := rs.Client.Get(ctx_timeout, adjacencyGraphKey).Result()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			// If the context times out error
			return emptyVal, fmt.Errorf("Redis Timeout: %w", err)
		} else if err == redis.Nil {
			// key doesn't exist
			return emptyVal, fmt.Errorf("Key not found: %w", err)
		} else {
			// Get from client error? maybe a retry?
			return emptyVal, err
		}
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

func (pg *PostgresGameState) FetchGS_sql(ctx context.Context, gameID, playerID string) (xCoords []string, yCoords []int, moveCounter int, err error) {
	var moveLogs []MoveLog

	if playerID == "all" { // this is called by the DeclaringMoveCommand handler
		result := pg.DB.Where("game_id = ?", gameID).Order("move_counter asc").Find(&moveLogs)
		if result.Error != nil {
			return nil, nil, 0, result.Error
		}
	} else { // this branch is called by the RecreateGS_Postgres and maybe another?
		result := pg.DB.Where("game_id = ? AND player_id = ?", gameID, playerID).Order("move_counter asc").Find(&moveLogs)
		if result.Error != nil {
			return nil, nil, 0, result.Error
		}
	}

	numMoves := len(moveLogs)

	xCoords = make([]string, numMoves)
	yCoords = make([]int, numMoves)

	for i, move := range moveLogs {
		xCoords[i] = move.xCoordinate
		yCoords[i] = move.yCoordinate
	}

	return xCoords, yCoords, moveCounter, nil
}
