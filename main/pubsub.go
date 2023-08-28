package main

import (
	"context"
	"fmt"
	"time"

	cache "github.com/go-redis/cache/v9"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	_ "github.com/mikeyg42/HEX/models"
)

// ...................... game Lock ......................//
const (	
	moveTimeout = 30 * time.Second

	shortDur = 1 * time.Second
)

var (
	ListenAddr = "??"
	RedisAddr  = "localhost:6379"
)






//.............. Main Characters ...............//




//................... MAIN .......................//
//................................................//

func main() {
	
	parentCtx, parentCancelFunc := context.WithCancel(context.Background())
	
	con, error := NewGameContainer(parentCtx, parentCancelFunc)
	if error != nil {
		panic(error)
	}

	// Initialize command and event dispatchers
	d := NewDispatcher(parentCtx, con)

	// initialize the workers and lobby
	lobbyCtx, lobbyCancel := context.WithCancel(parentCtx)
	numWorkers := 10
	con.StartNewWorkerPool(lobbyCtx, lobbyCancel, numWorkers)

	// Starts the command and event dispatchers's goroutines
	d.StartDispatcher(parentCtx)


}

//................................................//


func (con *GameContainer) GracefullyExiting() {

	// Close the Redis client = 
	err := con.Persister.redis.Client.Close()
	if err != nil {
		con.ErrorLog.ErrorLog(context.TODO(), "Error closing Redis client:", zap.Bool("close redis client", false), zap.Error(err))
	}

	// Close the Postgres client
	sqlDB, err := con.Persister.postgres.DB.DB()
	if err != nil {
		con.ErrorLog.ErrorLog(context.TODO(), "Error closing postgresql connection using generic sqlDB method", zap.Bool("close postgres connection", false), zap.Error(err))
	}
	sqlDB.Close()

	// flush logger queues I think?
	con.ErrorLog.ZapLogger.Sync()
	con.EventCmdLog.ZapLogger.Sync()
	
	// cancel the parent context, canceling all children too
	con.Exiter.parentCancelFunc()

}

//................................................//


func NewGameContainer(ctx context.Context, ctxCancelFunc context.CancelFunc) (*GameContainer, error) {
	eventCmdLogger := initLogger("/Users/mikeglendinning/projects/HEX/eventCommandLog.log", gormlogger.Info)
	errorLogger := initLogger("/Users/mikeglendinning/projects/HEX/errorLog.log", gormlogger.Info)
	
	eventCmdCtx := context.WithValue(ctx, eventCmdLoggerKey{}, eventCmdLogger)
	errorLogCtx := context.WithValue(ctx, errorLoggerKey{}, errorLogger)

	eventCmdLogger.ZapLogger.Sync()
	errorLogger.ZapLogger.Sync()

    // Initialize the RedisGameState, PostgresGameState
	gsp, RedisErr, PostgresErr := InitializePersistence(ctx)
	if gsp == nil {
		if RedisErr != nil {
			errorLogger.ErrorLog(ctx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "REDIS"))
		}
		if PostgresErr != nil {
			errorLogger.ErrorLog(ctx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "PostgresQL"))
		}
		return nil, fmt.Errorf("errors: logger: %v, redis: %v, postgres: %v", LogErr, RedisErr, PostgresErr)
	}

	eventCmdLogger.InfoLog(eventCmdCtx, "EventCmdLogger Initiated", zap.Bool("EventCmdLogger Activation", true))
	errorLogger.InfoLog(errorLogCtx, "ErrorLogger Initiated", zap.Bool("ErrorLogger Activation", true))

	exiter := &gracefulExit{
		parentCancelFunc: ctxCancelFunc,
	}

    timer := MakeNewTimer()
        
    return &GameContainer{
		Persister: 	 gsp,
        ErrorLog:    errorLogger,
		EventCmdLog: eventCmdLogger,
        Timer:       timer,
		Exiter: 	 exiter,
    }, nil
}