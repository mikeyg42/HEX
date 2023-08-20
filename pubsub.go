package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	cache "github.com/go-redis/cache/v9"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

//................................................//
//.................. STRUCTURES ..................//
//................................................//

// ................ events ......................//
var eventTypeMap = map[string]interface{}{
	"GameAnnouncementEvent":        &GameAnnouncementEvent{},
	"DeclaredMoveEvent":            &DeclaredMoveEvent{},
	"InvalidMoveEvent":             &InvalidMoveEvent{},
	"OfficialMoveEvent":            &OfficialMoveEvent{},
	"GameStateUpdate":              &GameStateUpdate{},
	"GameStartEvent":               &GameStartEvent{},
	"GameEndedEvent":               &GameEndedEvent{},
	"TimerON_StartTurnAnnounceEvt": &TimerON_StartTurnAnnounceEvt{},
}

type GameAnnouncementEvent struct {
	Event
	GameID         string    `json:"gameId"`
	FirstPlayerID  string    `json:"firstplayerID"`
	SecondPlayerID string    `json:"secondplayerID"`
	Timestamp      time.Time `json:"timestamp"`
}

type DeclaredMoveEvent struct {
	Event
	GameID    string    `json:"gameId"`
	PlayerID  string    `json:"playerId"`
	MoveData  string    `json:"moveData"`
	Timestamp time.Time `json:"timestamp"`
}

type InvalidMoveEvent struct {
	Event
	GameID      string `json:"gameId"`
	PlayerID    string `json:"playerId"`
	InvalidMove string `json:"moveData"`
}

type GameStartEvent struct {
	Event
	GameID         string
	FirstPlayerID  string
	SecondPlayerID string
	Timestamp      time.Time
}

type TimerON_StartTurnAnnounceEvt struct {
	Event
	GameID         string
	ActivePlayerID string
	TurnStartTime  time.Time
	TurnEndTime    time.Time
	MoveNumber     int
}

type OfficialMoveEvent struct {
	Event
	GameID    string    `json:"gameId"`
	PlayerID  string    `json:"playerId"`
	MoveData  string    `json:"moveData"`
	Timestamp time.Time `json:"timestamp"`
}

type GameStateUpdate struct {
	gorm.Model  `redis:"-"`
	Event       `redis:"-"`
	GameID      string
	MoveCounter int
	PlayerID    string
	xCoordinate string
	yCoordinate int

	ConcatenatedMove string `redis:"-"`
}

type GameEndedEvent struct {
	Event
	GameID       string `json:"gameId"`
	WinnerID     string `json:"winnerId"`
	LoserID      string `json:"loserId"`
	WinCondition string `json:"moveData"`
}

// ...................... game Lock ......................//
const moveTimeout = 30 * time.Second

// Define a struct to represent a locked game with two players
type LockedGame struct {
	WorkerID string
	GameID   string
	Player1  PlayerIdentity
	Player2  PlayerIdentity
}

// Define a shared map to keep track of locked games
var lockMutex sync.Mutex

//................ commands ......................//

var cmdTypeMap = map[string]interface{}{
	"DeclaringMoveCmd":    &DeclaringMoveCmd{},
	"LetsStartTheGameCmd": &LetsStartTheGameCmd{},
	"PlayerForfeitingCmd": &PlayerForfeitingCmd{},
	"UpdatingCmd":         &UpdatingCmd{},
	"StartNextTurnCmd":    &StartNextTurnCmd{},
	"NextTurnStartingCmd": &NextTurnStartingCmd{},
}

type NextTurnStartingCmd struct {
	Command
	GameID             string
	PriorPlayerID      string
	NextPlayerID       string
	UpcomingMoveNumber int
	TimeStamp          time.Time
}

type DeclaringMoveCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	Timestamp      time.Time `json:"timestamp"`
}

type CheckForWinConditionCmd struct {
	Command
	GameID    string    `json:"gameId"`
	MoveCount int       `json:"moveCount"`
	PlayerID  string    `json:"playerId"`
	Timestamp time.Time `json:"timestamp"`
}

type StartNextTurnCmd struct {
	Command
	gameID       string
	nextPlayerID string
	Timestamp    time.Time
}

type LetsStartTheGameCmd struct {
	Command
	GameID         string    `json:"gameId"`
	NextMoveNumber int       `json:"nextMoveNumber"`
	Player1        string    `json:"player1"`
	Player2        string    `json:"player2"`
	Timestamp      time.Time `json:"timestamp"`
}

type PlayerForfeitingCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	Timestamp      time.Time `json:"timestamp"`
	Reason         string    `json:"reason"`
}

// UNUSED FUNCTION ??
type UpdatingCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	Timestamp      time.Time `json:"timestamp"`
}

//.............. PERSISTENCE ....................//

type Event interface {
	// ModifyPersistedDataTable will be used by events to persist data to the database.
	ModifyPersistedDataTable(ctx context.Context) error
	MarshalEvt() ([]byte, error)
}

type Command interface {
	MarshalCmd() ([]byte, error)
}

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
	if LogErr != nil {
		errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "Logger"))
	}
	if RedisErr != nil {
		errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "REDIS"))
	}
	if PostgresErr != nil {
		errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false), zap.String("errorSource", "PostgresQL"))
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
//................ INITIALIZE ....................//
//................................................//

// Initialize Redis client
func getRedisClient(RedisAddress string) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     RedisAddress,
		Password: "",
		DB:       0, // Default DB
	})

	_, err := client.Ping(context.TODO()).Result()

	return client, err
}

//................................................//
//................. HANDLERS .....................//
//................................................//

// Event Handler Wrapper
func (d *Dispatcher) handleEventWrapper(event interface{}) {
	d.eventChan <- event
}

// Command Handler Wrapper
func (d *Dispatcher) handleCommandWrapper(cmd interface{}) {
	d.commandChan <- cmd
}

// Event Handler
func (d *Dispatcher) handleEvent(done <-chan struct{}, event Event, eventPubSub *redis.PubSub) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch event := event.(type) {
	case *InvalidMoveEvent:
		d.errorLogger.Warn(ctx, "Received InvalidMoveEvent: %v", event)


	case *OfficialMoveEvent:
		strs := []string{event.GameID, event.PlayerID, "has made a move:", event.MoveData}
		d.eventCmdLogger.Info(ctx, strings.Join(strs, "/"))

		// To get here means the player's move was valid! Therefore, we can stop their countdown. we will start new one after processing this move
		d.timer.StopTimer()

		// This will parse the new move and then publish another message to the event bus that will be picked up by persisting logic and the win condition logic
		newEvt := d.Handler_OfficialMoveEvt(ctx, event)
		d.EventDispatcher(newEvt)

	case *GameStateUpdate:
		// Persist the data and check win conditions
		newCmd := d.persister.HandleGameStateUpdate(event)

		d.CommandDispatcher(newCmd)

		d.timer.StopTimer() //always do this before starting timer again to ensure that the timer is flushed. It doesnt hurt to stop repeatedly.

	default:
		d.errorLogger.Error(ctx, "unknown event type: %v", fmt.Errorf("weird event type: %v", event))
		d.eventCmdLogger.Info(ctx, "Received Unusual, unknown event: %v", event)
	}
	return nil
}

func (d *Dispatcher) handleCommand(done <-chan struct{}, cmd Command, commandPubSub *redis.PubSub) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch cmd := cmd.(type) {
	case *DeclaringMoveCmd:
	
		// YOU NEED TO FINISH THIS 
		lockedGame, ok := allLockedGames[cmd.GameID]
		

		moveData := cmd.DeclaredMove
		moveParts := strings.Split(moveData, ".")
		new_key, _ := strconv.Atoi(moveParts[0])

		if new_key != lastPostgresCounter+1 {
			d.errorLogger.ErrorLog(ctx, "Unexpected moveCounter value from declaredMove", zap.Int("expected", lastPostgresCounter+1), zap.Int("received", new_key))
			return
		}

		new_val := moveParts[2] // new_val is the concatenated x and y coordinates of the most recent move
		xcoord := new_val[0]
		ycoord := new_val[1]

		pipe := d.persister.redis.Client.Pipeline()

		// Prepare commands for moves1 and moves2
		getMoves1 := pipe.HGetAll(ctx, fmt.Sprintf("MoveList:%s:%s", cmd.GameID, lockedGame.Player1.PlayerID))
		getMoves2 := pipe.HGetAll(ctx, fmt.Sprintf("MoveList:%s:%s", cmd.GameID, lockedGame.Player2.PlayerID))
		
		// Execute pipelined commands
		_, err := pipe.Exec(ctx)
		if err != nil {
			// handle error
		}
		
		// Fetch results
		moves1, err1 := getMoves1.Result()
		moves2, err2 := getMoves2.Result()
		if err1 != nil || err2 != nil {
			//l log error then go to postgresql
		}
		//combime moves1 and moves2
		for key, value := range moves2 {
			moves1[key] = value
		}
	
			

		coordMap, reversedMap, err := createCoordMaps(xcoords, ycoords)
		if err != nil {
			d.errorLogger.ErrorLog(ctx, "Error creating coordinate maps", zap.Error(err))
			return
		}

		// Verify move
		validMove := true
		if _, ok := coordMap[new_key]; ok {
			d.errorLogger.InfoLog(ctx, "Duplicate moveCounter in AcceptedMoves list", zap.String("DuplicateMoveNumber", moveData))
			validMove = false
		} else if _, found := reversedMap[new_val]; found {
			d.errorLogger.InfoLog(ctx, "Duplicate xy coord pair in AcceptedMoves list", zap.String("DuplicatePosition", moveData))
			validMove = false
		}

		// Dispatch Event based on validation
		var newEvent interface{}
		if !validMove {
			newEvent = &InvalidMoveEvent{
				GameID:      cmd.GameID,
				PlayerID:    cmd.SourcePlayerID,
				InvalidMove: cmd.DeclaredMove,
			}
		} else {
			newEvent = &OfficialMoveEvent{
				GameID:    cmd.GameID,
				PlayerID:  cmd.SourcePlayerID,
				MoveData:  cmd.DeclaredMove,
				Timestamp: time.Now(),
			}
		}
		d.EventDispatcher(newEvent)

	case *NextTurnStartingCmd:

		d.eventCmdLogger.Info(ctx, fmt.Sprintf("NextTurnStartingCmd received for game : %s",cmd.GameID))

		// Only Place you start the timer!
		d.timer.StartTimer()

		startTime := time.Now()
		endTime := startTime.Add(moveTimeout)

		newEvt := &TimerON_StartTurnAnnounceEvt{
			GameID:         cmd.GameID,
			ActivePlayerID: cmd.NextPlayerID,
			TurnStartTime:  startTime,
			TurnEndTime:    endTime,
			MoveNumber:     cmd.UpcomingMoveNumber,
		}
		d.EventDispatcher(newEvt)

	case *PlayerForfeitingCmd:

		d.eventCmdLogger.Info(ctx, "Player forfeited the game", zap.String("PlayerID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))

		d.timer.StopTimer()

		// Determine who the winner is via the allLockedGames struct
		lg := allLockedGames[cmd.GameID]
		var newEvt *GameEndedEvent
		
		winCond := []string{"Forfeit: ",cmd.Reason}
		winCondition := strings.Join(winCond,"")

		if cmd.SourcePlayerID == lg.Player1.PlayerID {
			newEvt = &GameEndedEvent{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: winCondition,
			}
		} else if cmd.SourcePlayerID == lg.Player2.PlayerID {
			newEvt = &GameEndedEvent{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: winCondition,
			}
		} else {
			d.errorLogger.ErrorLog(ctx, "Error parsing the winner from AllLockedGames global", zap.String("LoserID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))
		}
		d.EventDispatcher(newEvt)

	case *LetsStartTheGameCmd: // this command is generated by the LockTheGame function found in the workerpool code

		// Create a new channel for this game instance
		gameID := cmd.GameID

		// Check if the game is still locked (not claimed by another goroutine)
		lockMutex.Lock()
		lockedGame, ok := allLockedGames[gameID]
		delete(allLockedGames, gameID) // Remove the game from the map
		lockMutex.Unlock()

		if !ok {
			// The game was claimed by another goroutine, abort this instance
			return
		}

		// Announce the game with the locked players in a new goroutine
		startEvent := &GameStartEvent{
			GameID:         lockedGame.GameID,
			FirstPlayerID:  lockedGame.Player1.PlayerID,
			SecondPlayerID: lockedGame.Player2.PlayerID,
			Timestamp:      time.Now(),
		}

		d.EventDispatcher(startEvent)

		// MAKE THE TIMER AND START IT INITIALLY
		d.timer = MakeNewTimer()
		d.timer.startChan <- struct{}{}

	}
}

func (d *Dispatcher) parseOfficialMoveEvt(ctx context.Context, e *OfficialMoveEvent) (*GameStateUpdate, error) {
	// parses the OfficialMoveEvent and returns a GameStateUpdate struct, the next event in the progresssion, this struct is saveable in DB
	logger := d.errorLogger
	moveParts := strings.Split(e.MoveData, ".")

	if len(moveParts) != 3 {
		logger.ErrorLog(ctx, "moveData format error",
			zap.String("gameID", e.GameID),
			zap.String("moveData", e.MoveData),
		)
		return nil, fmt.Errorf("invalid moveData format")
	}

	moveCounter, err := strconv.Atoi(moveParts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid moveCounter: %w", moveParts[0], zap.Error(err))
	}

	gameID := e.GameID
	playerID := moveParts[1]

	coordinates := moveParts[2]
	if len(coordinates) < 2 {
		return nil, fmt.Errorf("missing at least part of the coordinates")
	}

	xCoordinate := coordinates[:1]
	yCoordinate, err := strconv.Atoi(coordinates[1:])
	if err != nil {
		return nil, fmt.Errorf("invalid yCoordinate: %w", err)
	}

	update := &GameStateUpdate{
		GameID:           gameID,
		PlayerID:         playerID,
		MoveCounter:      moveCounter,
		xCoordinate:      xCoordinate,
		yCoordinate:      yCoordinate,
		ConcatenatedMove: coordinates,
	}
	return update, nil
}

func (d *Dispatcher) Handler_OfficialMoveEvt(ctx context.Context, evt *OfficialMoveEvent) *GameStateUpdate {

	newEvt, err := d.parseOfficialMoveEvt(ctx, evt)
	if err != nil {
		d.errorLogger.ErrorLog(ctx, "error extracting move data", zap.Error(err))
	}
	return newEvt
}

func createCoordMaps(xCoords []string, yCoords []int) (map[int]string, map[string]int, error) {
	if len(xCoords) != len(yCoords) {
		return nil, nil, fmt.Errorf("Mismatched input lengths")
	}

	resultMap := make(map[int]string, len(xCoords))
	reverseMap := make(map[string]int, len(xCoords))

	for i, x := range xCoords {
		coordPair := []string{x, strconv.Itoa(yCoords[i])}
		resultMap[i+1] = strings.Join(coordPair, "")
		reverseMap[resultMap[i+1]] = i + 1
	}
	return resultMap, reverseMap, nil
}

