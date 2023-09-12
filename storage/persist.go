package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	cache "github.com/go-redis/cache/v9"
	hex "github.com/mikeyg42/HEX/models"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	retry "github.com/mikeyg42/HEX/retry"

)

var (
	ListenAddr = "??"
	RedisAddr  = "localhost:6379"
)

// .... INITIALIZE
func InitializePersistence(ctx context.Context) (*hex.GameStatePersister, error, error) {

	// create connection to postgres, and set up timeout functionality
	pgs, pgerr := InitializePostgres(ctx)
	if pgerr != nil {
		return nil, fmt.Errorf("InitError - Postgres"), nil
	}

	// create connection to redis, and set up context and timeout functionality differently
	rgs, rgerr := InitializeRedis(ctx)
	if rgerr != nil {
		return nil, nil, fmt.Errorf("InitError - Redis")
	}

	redisCache, cacherr := InitializeCache(rgs.Client)
	if cacherr != nil {
		return nil, nil, fmt.Errorf("InitError - Cache")
	}

	gsp := &hex.GameStatePersister{
		Postgres: pgs,
		Redis:    rgs,
		Cache:    redisCache,
	}

	return gsp, nil, nil
}

func InitializeRedis(ctx context.Context) (*hex.RedisGameState, error) {
	errorLogger, ok := ctx.Value(hex.ErrorLoggerKey{}).(*Logger)
	if !ok {
		return nil, fmt.Errorf("error getting errorlogger from context")
	}

	// make a connection to redis. we only need one of these per server
	redisClient, err := getRedisClient(RedisAddr, ctx) // this address is at the moment a global variable

	rgs := &hex.RedisGameState{
		Client:     redisClient,     // client
		Logger:     errorLogger,     // logger
		Context:    ctx,             // ctx
		TimeoutDur: 5 * time.Second, // duration
	}
	return rgs, err
}

// Initialize Redis client
func getRedisClient(RedisAddress string, ctx context.Context) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:        RedisAddress,
		Password:    "",
		DB:          0,                      // Default DB
		DialTimeout: 200 * time.Millisecond, // if Redis server gets broken, we specify the timeout for establishing the new connection
		ReadTimeout: 200 * time.Millisecond, // enables specifying a socket read timeout... In case any of the Redis server req reaches this timeout, the req will fail + not block server
	})

	_, err := client.Ping(ctx).Result()

	return client, err
}

func InitializePostgres(ctx context.Context) (*hex.PostgresGameState, error) {
	postgresLogger := &Logger{}
	postgresLogger = InitLogger("/Users/mikeglendinning/projects/HEX/postgresqlLOG.log", gormlogger.Info)

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
		DriverName: "postgres",
		PreferSimpleProtocol: true,
		
	}), &gorm.Config{
		Logger: postgresLogger,
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
	db.AutoMigrate(&hex.MoveLog{})

	// declare DB as a PostgresGameState
	return &hex.PostgresGameState{
		DB:         db,
		Logger:     postgresLogger,
		Context:    ctx, // still the same parentCtx!
		TimeoutDur: time.Second * 10,
	}, nil
}

func InitializeCache(redisClient *redis.Client) (*cache.Cache, error) {
	// currently I'm only using the cache for the declaringMoveCmd handler, to determine whether or not a move as been made already
	redisCache := cache.New(&cache.Options{
		Redis:      redisClient,
		LocalCache: cache.NewTinyLFU(1000, time.Minute),
	})

	ctx_timeout, cancelFunc := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancelFunc()

	// test for the cache!!
	testVal := make(map[int]string)
	testVal[0] = "HEX"

	if err := redisCache.Set(&cache.Item{
		Ctx:   ctx_timeout,
		Key:   "testKey",
		Value: testVal,
		TTL:   time.Second * 5, // TTL is the cache expiration time.
	}); err != nil {
		return nil, fmt.Errorf("cache failed to set a test value. error:", err.Error)
	}

	if err := redisCache.Get(ctx_timeout, "testKey", testVal[1]); err != nil {
		return nil, fmt.Errorf("cache test failed after initializing: %v", err)
	}

	if testVal[1] != "HEX" {
		return nil, fmt.Errorf("cache test failed after initializing! target save value: %s, actual save value: %s", testVal[0], testVal[1])
	}

	return redisCache, nil
}

// PERSIST functions

func PersistGraphToRedis(ctx context.Context, graph [][]int, gameID, playerID string, rs *hex.RedisGameState) error {
	key := fmt.Sprintf("adjGraph:%s:%s", gameID, playerID)
	serializedGraph, err := json.Marshal(graph)
	if err != nil {
		return fmt.Errorf("redis persist marshalling of graph error: %v", err)
	}

	rResult := retry.RetryFunc(ctx, func() error {
		return rs.Client.Set(ctx, key, serializedGraph, 0).Err()
	})

	if rResult.Err != nil {
		logger := retry.GetLoggerFromContext(ctx)
		logger.Error("Failed to persist move to Redis after retries", zap.Error(rResult.Err))
		return rResult.Err
	}

	return nil
}


func PersistMoveToRedisList(ctx context.Context, newMove hex.Vertex, gameID, playerID string, rs *hex.RedisGameState) error {
	moveKey := fmt.Sprintf("moveList:%s:%s", gameID, playerID)

	serializedMove, err := json.Marshal(newMove)
	if err != nil {
		return fmt.Errorf("failed to marshal new move: %w", err)
	}

	rResult := retry.RetryFunc(ctx, func() (string, error) {
		err := rs.Client.RPush(ctx, moveKey, serializedMove).Err()
		if err != nil {
			return "", err
		}
		return "Successfully persisted move to Redis", nil
	})

	if rResult.Err != nil {
		// Retrieve the logger and log the error
		logger := retry.GetLoggerFromContext(ctx)
		logger.Error("Failed to persist move to Redis after retries", zap.Error(rResult.Err))

		return rResult.Err
	}

	return nil
}

func PersistGameState_sql(ctx context.Context, update *hex.GameStateUpdate, pg *hex.PostgresGameState) error {

	txOptions := &sql.TxOptions{
		Isolation: sql.LevelSerializable,
	}

	tx := pg.DB.WithContext(ctx).Begin(txOptions)
	if tx.Error != nil {
		return tx.Error
	}

	moveLog := hex.MoveLog{
		GameID:      update.GameID,
		PlayerID:    update.PlayerID,
		MoveCounter: update.MoveCounter,
		XCoordinate: update.XCoordinate,
		YCoordinate: update.YCoordinate,
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
func FetchGameState (ctx context.Context, gameID, playerID string, gsp *hex.GameStatePersister, controller hex.GameStateControllerInterface) ([][]int, []hex.Vertex) {

	rs := gsp.Redis
	adjGraph, err := FetchAdjacencyGraph(ctx, gameID, playerID, rs)
	moveList, err2 := FetchPlayerMoves(ctx, gameID, playerID, rs)
	
	var moveCounter int = -1

	if err2 == nil {
		moveCounter = len(moveList)
	} else if err2 != nil && err == nil {
		moveCounter = len(adjGraph[0])

		// double check that the adj graph is a square for peace of mind
		if moveCounter != len(adjGraph) {
			panic(fmt.Errorf("adjGraph is not square"))
		}
	}
	
	if err != nil || err2 != nil {
		err := fmt.Errorf("Redis persistence failed, falling back to Postgres.")
		if moveCounter != -1 {
            adjGraph, moveList, err = controller.RecreateGS_Postgres(ctx, playerID, gameID, moveCounter)
			if err != nil {
				panic(err)
			}
		}
	}

	return adjGraph, moveList
}

func FetchPlayerMoves(ctx context.Context, gameID, playerID string, rs *hex.RedisGameState) ([]hex.Vertex, error) {
	// Construct the key for fetching the list of moves.
	movesKey := fmt.Sprintf("MoveList:%s:%s", gameID, playerID)

	emptyVal := []hex.Vertex{}
	ctx_timeout, cancel := context.WithTimeout(rs.Context, rs.TimeoutDur) // for example, 1 minute timeout
	defer cancel()

	// Fetch all moves from Redis using LRange.
	movesJSON, err := rs.Client.LRange(ctx_timeout, movesKey, 0, -1).Result()
	if err != nil {
		// log the error. maybe retry?
		return emptyVal, err
	}

	// Decode each move from its JSON representation.
	var moves []hex.Vertex
	for _, moveStr := range movesJSON {
		var move hex.Vertex
		err = json.Unmarshal([]byte(moveStr), &move)
		if err != nil {
			// Handle unmarshalling error somehow?
			return emptyVal, err
		}
		moves = append(moves, move)
	}

	return moves, nil
}

func FetchAdjacencyGraph(ctx context.Context, gameID, playerID string, rs *hex.RedisGameState) ([][]int, error) {
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
			return emptyVal, fmt.Errorf("redis failed because k ey not found: %w", err)
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

// moveCounter IS A SLICE OF INTEGERS WITH MAX LENGTH OF 230

func FetchGS_sql(ctx context.Context, gameID, playerID string, pg *hex.PostgresGameState) (xCoords []string, yCoords []int, moveCounter []int, err error) {
	var moveLogs []hex.MoveLog

	if playerID == "all" { // this is called by the DeclaringMoveCommand handler
		result := pg.DB.Where("game_id = ?", gameID).Order("move_counter asc").Find(&moveLogs)
		if result.Error != nil {
			moveCounter = make([]int, 0, 230)
			return nil, nil, moveCounter, result.Error
		}
	} else { // this branch is called by the RecreateGS_Postgres and maybe another?
		result := pg.DB.Where("game_id = ? AND player_id = ?", gameID, playerID).Order("move_counter asc").Find(&moveLogs)
		if result.Error != nil {
			moveCounter = make([]int, 0, 230)
			return nil, nil, moveCounter, result.Error
		}
	}

	numMoves := len(moveLogs)

	xCoords = make([]string, numMoves)
	yCoords = make([]int, numMoves)
	moveCounter = make([]int, numMoves, 230)

	for i, move := range moveLogs {
		xCoords[i] = move.XCoordinate
		yCoords[i] = move.YCoordinate
		moveCounter[i] = move.MoveCounter
	}

	return xCoords, yCoords, moveCounter, nil
}

func retrieveMoveList(ctx context.Context, gameID string, lg *hex.LockedGame, con *hex.Container) (map[int]string, error) {

	// 1. Try fetching from cache.
	cacheCtx, cancelCache := context.WithTimeout(ctx, 1*time.Second)
	defer cancelCache()
	moveList, err := FetchMoveListFromCache(cacheCtx, gameID, con)
	if err == nil && len(moveList) > 1 {
		return moveList, nil
	}
	con.ErrorLog.InfoLog(cacheCtx, "Cache could not retrieve Move List", zap.String("gameID", gameID), zap.Error(err))

	// 2. If cache misses, try fetching from Redis.
	redisCtx, cancelRedis := context.WithTimeout(ctx, 2*time.Second)
	defer cancelRedis()
	moveList, err = fetchMoveListFromRedis(redisCtx, gameID, con)
	if err == nil && len(moveList) > 1 {
		return moveList, nil
	}
	con.ErrorLog.InfoLog(redisCtx, "Redis could not retrieve Move List", zap.String("gameID", gameID), zap.Error(err))
	cancelRedis()

	// 3. If Redis fails, try fetching from Postgres.
	pgCtx, cancelPg := context.WithTimeout(ctx, 3*time.Second)
	defer cancelPg()
	moveList, err = fetchMoveListFromPostgres(pgCtx, gameID, con)
	if err != nil {
		return nil, err // If even Postgres fails, return the error.
	}
	return moveList, nil
}

func FetchMoveListFromCache(ctx context.Context, gameID string, con *hex.Container) (map[int]string, error) {
	cacheKey := fmt.Sprintf("cache:%d:movelist", gameID)
	moveList := make(map[int]string)
	if err := con.Persister.Cache.Get(ctx, cacheKey, moveList); err != nil {
		return nil, fmt.Errorf("retrieve from cache failed!! %w", err)
	}
	return moveList, nil
}

func fetchMoveListFromRedis(ctx context.Context, gameID string, con *hex.Container) (map[int]string, error) {
	
	lg := hex.AllLockedGames[gameID]

	ml1, err1 := FetchPlayerMoves(ctx, gameID, lg.Player1.PlayerID, con.Persister.Redis)
	ml2, err2 := FetchPlayerMoves(ctx, gameID, lg.Player2.PlayerID, con.Persister.Redis)

	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("error fetching move list from redis", errors.Join(err1, err2))
	}
	fetchedMoves := make(map[int]string)

	// NOTE: the movenumber is going to be bullshit here, but it does not matter
	counter := 0
	for _, v := range append(ml1, ml2...) {
		combinedCoord := fmt.Sprintf("%d,%d", v.X, v.Y)
		fetchedMoves[counter] = combinedCoord
		counter++
	}

	if len(fetchedMoves) != len(ml1)+len(ml2) {
		return nil, fmt.Errorf("redis move list failed to be fetched and properly reformatting")
	}

	return fetchedMoves, nil
}

func fetchMoveListFromPostgres(ctx context.Context, gameID string, con *hex.Container) (map[int]string, error) {
	xCoords, yCoords, moveNumber, err := FetchGS_sql(ctx, gameID, "all", con.Persister.Postgres)
	if err != nil {
		return nil, err
	}

	fetchedMoves := make(map[int]string)
	for i := 0; i < len(xCoords); i++ {
		combinedCoord := fmt.Sprintf("%d, %d", xCoords[i], yCoords[i])
		fetchedMoves[moveNumber[i]] = combinedCoord
	}
	return fetchedMoves, nil
}
