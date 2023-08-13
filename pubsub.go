package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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
	"GameAnnouncementEvent": &GameAnnouncementEvent{},
	"DeclaredMoveEvent":     &DeclaredMoveEvent{},
	"InvalidMoveEvent":      &InvalidMoveEvent{},
	"OfficialMoveEvent":     &OfficialMoveEvent{},
	"GameStateUpdate":       &GameStateUpdate{},
	"GameStartEvent":        &GameStartEvent{},
	"NextTurnStartsEvent":   &NextTurnStartsEvent{},
}

type NextTurnStartsEvent struct {
	Event
	GameID             string
	PriorPlayerID      string
	NextPlayerID       string
	UpcomingMoveNumber int
	TimeStamp          time.Time
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
	GameID         string
	FirstPlayerID  string
	SecondPlayerID string
	Timestamp      time.Time
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
	"EndingGameCmd":         &EndingGameCmd{},
}
type EndingGameCmd struct {
	Command
	GameID       string `json:"gameId"`
	WinnerID     string `json:"winnerId"`
	LoserID      string `json:"loserId"`
	WinCondition string `json:"moveData"`
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

// maybe should be  an event
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
	DB     *gorm.DB
	logger Logger
}

var (
	ListenAddr = "??"
	RedisAddr  = "localhost:6379"
)

type RedisGameState struct {
	client *redis.Client
	logger Logger
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

	client, err := getRedisClient(RedisAddr)
	if err != nil {
		errorLogger.ErrorLog(parentCtx, "Error getting Redis Client", zap.Bool("get redisClient", false))
		return
	}

	gsp, err := InitializePersistence(persistCtx)
	if err != nil {
		errorLogger.ErrorLog(parentCtx, "Error initializing persistence", zap.Bool("initialize persistence", false))
	}

	// Initialize command and event dispatchers
	d := NewDispatcher(parentCtx, gsp, errorLogger, eventCmdLogger)

	// Starts the command and event dispatchers's goroutines
	d.Start(parentCtx)

	//....... some stuff?

	// flush logger queues I think?
	eventCmdLogger.ZapLogger.Sync()
	errorLogger.ZapLogger.Sync()
	persistLogger.ZapLogger.Sync()

	// Close the Redis client
	err = client.Close()
	if err != nil {
		errorLogger.ErrorLog(parentCtx, "Error closing Redis client:", zap.Bool("close redis client", false), zap.Error(err))
	}

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
		Password: "", // Redis password if required
		DB:       0,  // Default DB
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
		d.eventCmdLogger.Info(ctx, event.GameID+"/"+event.PlayerID+" has made a move: "+event.MoveData)

		// To get here means the player's move was valid! Therefore, we can stop their countdown. we will start new one after processing this move
		d.timer.StopTimer()

		// This will parse the new move and then publish another message to the event bus that will be picked up by persisting logic and the win condition logic
		newEvt := d.Handler_OfficialMoveEvt(ctx, event)
		d.EventDispatcher(newEvt)

	case *GameStateUpdate:
		// Persist the data and check win conditions
		newEvt, yesEvt := d.persister.HandleGameStateUpdate(event)
		
		// If yesEvt is true, then the handler will output an event, namely, nextTurnStartsEvent	
		if yesEvt {
			d.EventDispatcher(newEvt)
		} else {
			d.CommandDispatcher(newEvt)
		}
		d.timer.StopTimer() //always do this before starting timer again to ensure that the

	case *NextTurnStartsEvent:
		d.eventCmdLogger.Info(ctx, "NextTurnStartsEvent received for game: "+event.GameID)
		d.timer.StartTimer()

	default:
		d.errorLogger.Error(ctx, "Unknown event type: %v", fmt.Errorf("Weird event type: %T", event))
		d.eventCmdLogger.Info(ctx, "Received Unusual, unknown event: %v", event)
	}
	return nil
}

func (d *Dispatcher) handleCommand(done <-chan struct{}, cmd Command, commandPubSub *redis.PubSub) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch cmd := cmd.(type) {

	case *GameEndingCmd:
		d.timer.StopTimer()
		d.eventCmdLogger.Info(ctx, cmd.GameID+" is over", zap.String("Winner", cmd.WinnerID), zap.String("Loser", cmd.LoserID), zap.String("WinCondition", cmd.WinCondition))

		// clear redis 
		

		gracefullyShutDownGame()
		commandPubSub.Close()

	case *DeclaringMoveCmd:
		xcoords, ycoords, lastPostgresCounter, err := d.persister.postgres.FetchGS_sql(ctx, cmd.GameID, "all")
		if err != nil {
			d.persister.postgres.logger.ErrorLog(ctx, "Error fetching game state from persistence", zap.Error(err))
			return
		}

		moveData := cmd.DeclaredMove
		moveParts := strings.Split(moveData, ".")
		new_key, _ := strconv.Atoi(moveParts[0])

		if new_key != lastPostgresCounter+1 {
			d.errorLogger.ErrorLog(ctx, "Unexpected moveCounter value from declaredMove", zap.Int("expected", lastPostgresCounter+1), zap.Int("received", new_key))
			return
		}

		new_val := moveParts[2]
		validMove := true

		coordMap, reversedMap, err := createCoordMaps(xcoords, ycoords)
		if err != nil {
			d.errorLogger.ErrorLog(ctx, "Error creating coordinate maps", zap.Error(err))
			return
		}

		// Verify move
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

	case *PlayerForfeitingCmd:

		d.eventCmdLogger.Info(ctx, "Player forfeited the game", zap.String("PlayerID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))

		d.timer.StopTimer()

		// Determine who the winner is via the allLockedGames struct and the PlayerForfeitingCmd
		lg := allLockedGames[cmd.GameID]
		var newCmd *EndingGameCmd

		if cmd.SourcePlayerID == lg.Player1.PlayerID {
			newCmd = &EndingGameCmd{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: "Forfeit: " + cmd.Reason,
			}
		} else if cmd.SourcePlayerID == lg.Player2.PlayerID {
			newCmd = &EndingGameCmd{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: "Forfeit: " + cmd.Reason,
			}
		} else {
			d.errorLogger.ErrorLog(ctx, "Error parsing the winner from AllLockedGames global", zap.String("LoserID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))
		}
		d.CommandDispatcher(newCmd)
 
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
		return nil, fmt.Errorf("invalid coordinates format")
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
		coordPair := x + strconv.Itoa(yCoords[i])
		resultMap[i+1] = coordPair
		reverseMap[coordPair] = i + 1
	}
	return resultMap, reverseMap, nil
}
