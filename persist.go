package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"
	"encoding/json"

	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// ..... INITIALIZE BOTH DATASTORES
/* 
// functions that are called once prior to any game starting!
func InitializePostgres(ctx context.Context) (*PostgresGameState, error) {
	logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
	if !ok {
		return nil, fmt.Errorf("error getting logger from context")
	}

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
		Logger: logger,
	})
	if err != nil {
		logger.ErrorLog(ctx, "CANNOT CONNECT TO GORM DB", zap.Error(err))
		return nil, err
	}

	// sqlDB is a lower level of abstraction but is a reference to the same underlying sql.DB struct that GORM is using, so modifying sqlDB changes the gorm postgres DB
	sqlDB, err := db.DB()
	if err != nil {
		logger.ErrorLog(ctx, "error defining lower-level referece to database/sql", zap.Error(err))
		return nil, err
	}
	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(200)
	sqlDB.SetConnMaxIdleTime(time.Minute * 5)

	// using automigrate with this empty gamestateupdate creates the relational database table for us if it doesn't already exist
	db.AutoMigrate(&GameStateUpdate{})

	// declare DB as a PostgresGameState
	return &PostgresGameState{DB: db}, nil
} */

/* func createGameStatePersist(ctx context.Context, redisClient *redis.Client) (*GameStatePersister, error) {
	pgs, err := InitializePostgres(ctx)
	return &GameStatePersister{
		postgres: pgs,
		redis:    &RedisGameState{redisClient},}, err
} */
/* 
// ..... SAVE A MOVE TO BOTH DATASTORES
func (gsp *GameStatePersister) PersistMove(ctx context.Context, newMove *GameStateUpdate) error {

	// Convert xCoordinate and yCoordinate to a Vertex type.
	vertex, err := convertToTypeVertex(newMove.xCoordinate, newMove.yCoordinate)
	if err != nil {
		return err
	}

	// Key structure: "GameID:PlayerID:Moves" and "GameID:PlayerID:AdjacencyGraph"
	movesKey := fmt.Sprintf("%s:%s:Moves", newMove.GameID, newMove.PlayerID)
	adjacencyGraphKey := fmt.Sprintf("%s:%s:AdjacencyGraph", newMove.GameID, newMove.PlayerID)

	// Convert the Vertex to JSON.
	vertexJSON, err := json.Marshal(vertex)
	if err != nil {
		return err
	}

	// Here you should create and update the adjacency graph for the move.
	// This example assumes a placeholder adjacency graph.
	updatedAdjacencyGraph := [][]int{
		// ... the updated adjacency graph
	}

	adjacencyGraphJSON, err := json.Marshal(updatedAdjacencyGraph)
	if err != nil {
		return err
	}

	// Use a pipeline for atomicity.
	pipe := gsp.redis.client.Pipeline()
	pipe.RPush(ctx, movesKey, vertexJSON)                // Add the new move to the moves list.
	pipe.Set(ctx, adjacencyGraphKey, adjacencyGraphJSON) // Overwrite the previous adjacency graph.

	_, err = pipe.Exec(ctx)
	return err
}


// ..... FETCH DATASTORES FOR A GAME

// FetchGameStateUpdates fetches FROM POSTGRES all game state updates for a game and returns them as an AcceptedMoves map
func (gsp *GameStatePersister) FetchFromPostgres(ctx context.Context, gameID string) ([]string, error) {
	var updates []GameStateUpdate

	// Fetch the records
	if err := gsp.postgres.DB.Where("game_id = ?", gameID).Find(&updates).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch data: %w", err)
	}
	allMoves := make([]string, len(updates))

	for _, update := range updates {
		// Convert YCoordinate to a string and concatenate XCoordinate and YCoordinate to form the map's value
		yCoordinateStr := strconv.Itoa(update.yCoordinate)
		concatenatedMoveData := update.xCoordinate + yCoordinateStr

		// Use moveCounter as the key
		allMoves[update.MoveCounter] = concatenatedMoveData
	}

	err := checkDatastoreSize(allMoves)

	return allMoves, err
}

func (gsp *GameStatePersister) FetchFromRedis(ctx context.Context, gameID string) ([]string, error) {
	trackerKey = gameID + "tracker"
	redisMoveCounter, err := gsp.redis.client.Get(ctx, trackerKey).Result()
	
	movesKey := gameID + "list"
	redisMoveList, err := gsp.redis.client.HGetAll(ctx, movesKey).Result()
	if err != nil {
		//d.errorLogger.ErrorLog(ctx, "error extracting cache", zap.Error(err))
		return nil, fmt.Errorf("error extracting cache", zap.Error(err))
	}

	allMoves := make([]string, len(redisMoveList))
	for field, value := range redisMoveList {
		f, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("error converting cache hash to slice: %w", err)
		}
		allMoves[f] = value
	}

	err = checkDatastoreSize(allMoves)

	return allMoves, err
} */



