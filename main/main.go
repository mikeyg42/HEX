package main

import (
	"context"
	"fmt"
	"time"

	hex "github.com/mikeyg42/HEX/models"
	storage "github.com/mikeyg42/HEX/storage"
	zap "go.uber.org/zap"
	gormlogger "gorm.io/gorm/logger"
)

const (
	moveTimeout = 30 * time.Second

	shortDur = 1 * time.Second

	numInitialWorkers = 10
)

func main() {

	parentCtx, parentCancelFunc := context.WithCancel(context.Background())

	con, error := NewContainer(parentCtx, parentCancelFunc)
	if error != nil {
		panic(error)
	}

	// Initialize command and event dispatchers
	d, doneDispatcher := NewDispatcher(parentCtx, con)

	// initialize the workers and lobby
	lobbyCtx := parentCtx

	//................................................//
	// placeholder for the initialize workers and lobby code
	//................................................//

	// Starts the command and event dispatchers's goroutines
	d.StartDispatcher(parentCtx)

	// do some more stuff

	// exit and close everything
	GracefullyExit(con, doneDispatcher)
}

//................................................//

func GracefullyExit(con *hex.Container, doneDispatcher chan struct{}) {
	
	doneDispatcher <- struct{}{}
	close(doneDispatcher)// send a signal to the dispatcher to stop
	
	// Close the Redis client =
	err := con.Persister.Redis.Client.Close()
	if err != nil {
		con.ErrorLog.ErrorLog(context.TODO(), "Error closing Redis client:", zap.Bool("close redis client", false), zap.Error(err))
	}

	// Close the Postgres client
	sqlDB, err := con.Persister.Postgres.DB.DB()
	if err != nil {
		con.ErrorLog.ErrorLog(context.TODO(), "Error closing postgresql connection using generic sqlDB method", zap.Bool("close postgres connection", false), zap.Error(err))
	}
	sqlDB.Close()

	// flush logger queues I think?
	con.ErrorLog.Sync()
	con.EventCmdLog.Sync()

	// cancel the parent context, canceling all children too, which includes all the logs
	con.CtxExiterFn.ParentCancelFunc()

}

//................................................//

func NewContainer(ctx context.Context, ctxCancelFunc context.CancelFunc) (*hex.Container, error) {
	eventCmdLogger := storage.InitLogger("/Users/mikeglendinning/projects/HEX/eventCommandLog.log", gormlogger.Info)
	errorLogger := storage.InitLogger("/Users/mikeglendinning/projects/HEX/errorLog.log", gormlogger.Info)

	eventCmdCtx := context.WithValue(ctx, hex.EventCmdLoggerKey{}, &eventCmdLogger)
	errorLogCtx := context.WithValue(ctx, hex.ErrorLoggerKey{}, &errorLogger)

	// Initialize the RedisGameState, PostgresGameState
	gsp, RedisErr, PostgresErr := storage.InitializePersistence(ctx)
	if gsp == nil {
		if RedisErr != nil {
			errorLogger.ErrorLog(ctx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "REDIS"))
		}
		if PostgresErr != nil {
			errorLogger.ErrorLog(ctx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "PostgresQL"))
		}
		return nil, fmt.Errorf("errors: redis: %v, postgres: %v", RedisErr, PostgresErr)
	}

	eventCmdLogger.InfoLog(eventCmdCtx, "EventCmdLogger Initiated", zap.Bool("EventCmdLogger Activation", true))
	errorLogger.InfoLog(errorLogCtx, "ErrorLogger Initiated", zap.Bool("ErrorLogger Activation", true))

	exiter := &hex.ContextMaster_exiter{
		ParentCancelFunc: ctxCancelFunc,
	}

	return &hex.Container{
		Persister:   gsp,
		ErrorLog:    errorLogger,
		EventCmdLog: eventCmdLogger,
		CtxExiterFn: exiter,
	}, nil
}
