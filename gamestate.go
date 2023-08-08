package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
}

func createGameStatePersist(ctx context.Context, redisClient *redis.Client) (*GameStatePersister, error) {

	pgs, err := InitializePostgres(ctx)

	return &GameStatePersister{
		postgres: pgs,
		redis:    &RedisGameState{redisClient}}, err
}

func (gsp *GameStatePersister) PersistMove(move *GameStateUpdate) error {
	ctx := context.Background()

	// Start pipelining.
	pipe := gsp.redis.client.Pipeline()

	// Set fields in a hash map for a given aggregateID
	pipe.HSet(ctx, move.GameID, "playerID", move.PlayerID)
	pipe.HSet(ctx, move.GameID, "moveCounter", move.MoveCounter)
	pipe.HSet(ctx, move.GameID, "xCoordinate", move.xCoordinate)
	pipe.HSet(ctx, move.GameID, "yCoordinate", move.yCoordinate)

	// Execute the pipeline
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("error executing redis pipeline to update cache: %w", err)
	}

	err = gsp.postgres.DB.Create(move).Error
	if err != nil {
		return fmt.Errorf("error updating game state in postgres: %w", err)
	}

	return nil
}

// FetchGameStateUpdates fetches FROM POSTGRES all game state updates for a game and returns them as an AcceptedMoves map
func (gsp *GameStatePersister) FetchFromPostgres(ctx context.Context, gameID string) (*AcceptedMoves, error) {
	var updates []GameStateUpdate

	// Fetch the records
	if err := gsp.postgres.DB.Where("game_id = ?", gameID).Order("move_counter").Find(&updates).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch data: %w", err)
	}

	// Process the records to create the AcceptedMoves map
	acceptedMoves := &AcceptedMoves{list: make(map[int]string)}

	for _, update := range updates {
		// Convert YCoordinate to a string and concatenate XCoordinate and YCoordinate to form the map's value
		yCoordinateStr := strconv.Itoa(update.yCoordinate)
		concatenatedMoveData := update.xCoordinate + yCoordinateStr

		// Use moveCounter as the key
		acceptedMoves.list[update.MoveCounter] = concatenatedMoveData
	}

	return acceptedMoves, nil
}

func (gsp *GameStatePersister) FetchFromRedis(ctx context.Context, gameID string) (*AcceptedMoves, error) {

	redisMoveList, err := gsp.redis.client.HGetAll(ctx, gameID).Result()
	if err != nil {
		//d.errorLogger.ErrorLog(ctx, "error extracting cache", zap.Error(err))
		return nil, fmt.Errorf("error extracting cache", zap.Error(err))
	}
	numElements := len(redisMoveList)
	// Convert the result to a Go map
	allMoves := &AcceptedMoves{list: make(map[int]string, numElements)}

	for field, value := range redisMoveList {
		f, err := strconv.Atoi(field)
		if err != nil {
			//d.errorLogger.ErrorLog(ctx, "error converting cache hash to Go map", zap.Error(err))
			return nil, fmt.Errorf("error converting cache hash to Go map", zap.Error(err))
		}
		allMoves.list[f] = value
	}

	maxCounter := largestKey(allMoves.list)

	if numElements != maxCounter {
		//d.errorLogger.ErrorLog(ctx, "error querying cache", zap.Error(fmt.Errorf("numElements != maxMoveCounter")))
		return nil, fmt.Errorf("error querying cache", zap.Error(fmt.Errorf("numElements != maxMoveCounter")))
	}
	return allMoves, nil
}

func largestKey(m map[int]string) int {
	maxKey := 0
	for k := range m {
		if k > maxKey {
			maxKey = k
		}
	}
	return maxKey
}

func (gsp *GameStatePersister) FetchGS(gameID string) (*AcceptedMoves, error) {
	// Try to fetch from Redis first
	data, err := gsp.FetchFromRedis(context.Background(), gameID)
	if err != nil {
		// If failed to fetch from Redis, try to fetch from Postgres
		data, err = gsp.FetchFromPostgres(context.Background(), gameID)
		if err != nil {
			return nil, err
		}

		// If fetched from Postgres,  confirm that it is goood like you did with redis then update Redis
	}

	return data, nil
}
func (d *Dispatcher) getCacheSize(key string) (int, error) {
	ctx := context.Background()
	//numElements, err := d.client.HLen(context.Background(), key).Result()
	allKeys, err := (d.client.HKeys(context.Background(), key)).Result()
	if err != nil {
		d.errorLogger.ErrorLog(ctx,"error querying caches key list", err)
		return -1, nil
	}

	numElements := len(allKeys)
	maxCounter := 0
	for p := range allKeys {
		pp, _ := strconv.Atoi(allKeys[p])
		if pp > maxCounter {
			maxCounter = pp
		}
	}

	if int(numElements) != maxCounter {
		d.errorLogger.ErrorLog(ctx,"error querying cache", fmt.Errorf("numElements != maxMoveCounter"))
		return -1, nil
	}

	return maxCounter, nil
}
