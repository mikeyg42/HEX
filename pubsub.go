package main

import (
	"context"
	"sync"
	"time"

	cache "github.com/go-redis/cache/v9"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ...................... game Lock ......................//
const moveTimeout = 30 * time.Second

// Define a struct to represent a locked game with two players
type LockedGame struct {
	WorkerID  string
	GameID    string
	FwdPlayer PlayerIdentity // until after second turn there won't be a player 1 and 2, because of the swap mechanic
	RevPlayer PlayerIdentity
	Player1   PlayerIdentity
	Player2   PlayerIdentity
}

// Define a shared map to keep track of locked games
var lockMutex sync.Mutex

//.............. PERSISTENCE ....................//

const shortDur = 1 * time.Second

type PostgresGameState struct {
	DB         *gorm.DB
	Logger     Logger
	Context    context.Context
	TimeoutDur time.Duration
}

var (
	ListenAddr = "??"
	RedisAddr  = "localhost:6379"
)

type RedisGameState struct {
	Client     *redis.Client
	MyCache    *cache.Cache
	Logger     Logger
	Context    context.Context
	TimeoutDur time.Duration
}

type GameStatePersister struct {
	postgres *PostgresGameState
	redis    *RedisGameState
	cache    *cache.Cache
}

//.............. Main Characters ...............//

// Dispatcher represents the combined event and command dispatcher.
type Dispatcher struct {
	commandChan     chan interface{}
	eventChan       chan interface{}
	errorLogger     Logger
	eventCmdLogger  Logger
	commandHandlers map[string]func(interface{})
	eventHandlers   map[string]func(interface{})
	persister       *GameStatePersister
	timer           *TimerControl
}

type Worker struct {
	WorkerID    string
	GameID      string
	PlayerChan  chan PlayerIdentity
	ReleaseChan chan string // Channel to notify the worker to release the player
}

type PlayerIdentity struct {
	PlayerID          string
	CurrentGameID     string
	CurrentOpponentID string
	CurrentPlayerRank int
	Username          string // Player's username (this is their actual username, whereas PlayerID will be a UUID+player1 or UUID+player2)
}

//.............. Logger keys ....................//

type eventCmdLoggerKey struct{}
type errorLoggerKey struct{}
type persistLoggerKey struct{}

//................................................//
//................... MAIN .......................//
//................................................//

func main() {
	eventCmdLogger := initLogger("/Users/mikeglendinning/projects/HEX/eventCommandLog.log", gormlogger.Info)
	errorLogger := initLogger("/Users/mikeglendinning/projects/HEX/errorLog.log", gormlogger.Info)
	persistLogger := initLogger("/Users/mikeglendinning/projects/HEX/persistLog.log", gormlogger.Info)

	parentCtx, parentCancelFunc := context.WithCancel(context.Background())
	eventCmdCtx := context.WithValue(parentCtx, eventCmdLoggerKey{}, eventCmdLogger)
	errorLogCtx := context.WithValue(parentCtx, errorLoggerKey{}, errorLogger)
	persistCtx := context.WithValue(parentCtx, persistLoggerKey{}, persistLogger)

	eventCmdLogger.ZapLogger.Sync()
	errorLogger.ZapLogger.Sync()
	persistLogger.ZapLogger.Sync()

	eventCmdLogger.InfoLog(eventCmdCtx, "EventCmdLogger Initiated", zap.Bool("EventCmdLogger Activation", true))
	errorLogger.InfoLog(errorLogCtx, "ErrorLogger Initiated", zap.Bool("ErrorLogger Activation", true))

	gsp, LogErr, RedisErr, PostgresErr := InitializePersistence(persistCtx)
	if gsp == nil {
		if LogErr != nil {
			errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "Logger"))
		}
		if RedisErr != nil {
			errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "REDIS"))
		}
		if PostgresErr != nil {
			errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "PostgresQL"))
		}
	}

	// Initialize command and event dispatchers
	d := NewDispatcher(parentCtx, gsp, errorLogger, eventCmdLogger)

	// initialize the workers and lobby
	lobbyCtx, lobbyCancel := context.WithCancel(parentCtx)
	numWorkers := 10
	d.StartNewWorkerPool(lobbyCtx, lobbyCancel, numWorkers)

	// Starts the command and event dispatchers's goroutines
	d.StartDispatcher(parentCtx)

	//....... some stuff? this should all go in the graceful shutdown function

	// flush logger queues I think?
	eventCmdLogger.ZapLogger.Sync()
	errorLogger.ZapLogger.Sync()
	persistLogger.ZapLogger.Sync()

	// Close the Redis client
	err := gsp.redis.Client.Close()
	if err != nil {
		errorLogger.ErrorLog(parentCtx, "Error closing Redis client:", zap.Bool("close redis client", false), zap.Error(err))
	}

	// Close the Postgres client
	sqlDB, err := gsp.postgres.DB.DB()
	if err != nil {
		errorLogger.ErrorLog(parentCtx, "Error closing postgresql connection using generic sqlDB method", zap.Bool("close postgres connection", false), zap.Error(err))
	}
	sqlDB.Close()

	// cancel the parent context, canceling all children too
	parentCancelFunc()
}

//................................................//
//................................................//
