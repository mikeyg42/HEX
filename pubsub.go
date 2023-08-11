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
	"GameEndsEvent":         &GameEndsEvent{},
	"GameStartEvent":        &GameStartEvent{},
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
	AcceptedMoves *AcceptedMoves	`json:"acceptedMoves"`
}

type GameEndsEvent struct {
	Event
	GameID       string `json:"gameId"`
	WinnerID     string `json:"winnerId"`
	LoserID      string `json:"loserId"`
	WinCondition string `json:"moveData"`
}

type GameStateUpdate struct {
	gorm.Model		   `redis:"-"`
	Event			   `redis:"-"`
	GameID      string 
	MoveCounter int    
	PlayerID    string 
	xCoordinate string
	yCoordinate int

	ConcatenatedMove string	`redis:"-"`
}

/* type AcceptedMoves struct {
	list map[int]string
} */

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
	DB *gorm.DB
}

var (
	ListenAddr = "??"
	RedisAddr  = "localhost:6379"
)

type RedisGameState struct {
	client *redis.Client
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
	client          *redis.Client
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

//................................................//
//................... MAIN .......................//
//................................................//

func main() {
	eventCmdLogger := initLogger("/Users/mikeglendinning/projects/HEX/eventCommandLog.log", gormlogger.Info)
	errorLogger := initLogger("/Users/mikeglendinning/projects/HEX/errorLog.log", gormlogger.Info)

	eventCmdCtx := context.WithValue(context.Background(), eventCmdLoggerKey{}, eventCmdLogger)
	ctx := context.WithValue(eventCmdCtx, errorLoggerKey{}, errorLogger)

	eventCmdLogger.ZapLogger.Sync()
	errorLogger.ZapLogger.Sync()

	eventCmdLogger.InfoLog(eventCmdCtx, "EventCmdLogger Initiated", zap.Bool("EventCmdLogger Activation", true))
	errorLogger.InfoLog(ctx, "ErrorLogger Initiated", zap.Bool("ErrorLogger Activation", true))

	client, err := getRedisClient(RedisAddr)
	if err != nil {
		errorLogger.ErrorLog(ctx, "Error getting Redis Client", zap.Bool("get redisClient", false))
		return
	}

	// Initialize command and event dispatchers
	d := NewDispatcher(ctx, client, errorLogger, eventCmdLogger)

	// Starts the command and event dispatchers's goroutines
	d.Start(ctx)

	//....... some stuff?

	eventCmdLogger.ZapLogger.Sync()
	errorLogger.ZapLogger.Sync()

	// Close the Redis client
	err = client.Close()
	if err != nil {
		errorLogger.ErrorLog(ctx, "Error closing Redis client:", zap.Error(err))
	}
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
func (d *Dispatcher) handleEvent(ctx context.Context, done <-chan struct{}, event Event, eventPubSub *redis.PubSub) error {

	switch event := event.(type) {
	case *InvalidMoveEvent:
		d.errorLogger.Warn(ctx, "Received InvalidMoveEvent: %v", event)

	case *OfficialMoveEvent:
		d.eventCmdLogger.Info(ctx, event.GameID+"/"+event.PlayerID+" has made a move: "+event.MoveData)

		// To get here means the player's move was valid! Therefore, we can stop their countdown. we will start new one after processing this move
		d.timer.StopTimer()

		// This will parse the new move and then publish another message to the event bus that will be picked up by persisting logic and the win condition logic
		newEvt := d.WrappedHandlerforEachlMoveEvent(ctx, event)
		d.EventDispatcher(newEvt)

	case *GameEndsEvent:

		d.eventCmdLogger.Info(ctx, event.GameID+" is over", zap.String("Winner", event.WinnerID), zap.String("Loser", event.LoserID), zap.String("WinCondition", event.WinCondition))

		// Persist the game end to a database
		err := event.ModifyPersistedDataTable()
		if err != nil {
			d.errorLogger.Error(ctx, "Error persisting games end move: %v", err)
		}
		eventPubSub.Close()

	case *GameStateUpdate:
		// Persist the data and check win conditions
		err := d.persister.PersistMove(event)
		if err != nil {
			d.errorLogger.ErrorLog(ctx, "Error updating game state persistence: %v", zap.Error(err))
		}

		newCmd := &CheckForWinConditionCmd{
			GameID:    event.GameID,
			MoveCount: event.MoveCounter,
			PlayerID:  event.PlayerID,
			Timestamp: time.Now(),
		}

		d.CommandDispatcher(newCmd)

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
	case *DeclaringMoveCmd:

		//key := "acceptedMoveList" // key is for accessing the cache

		acceptedMoves, err := d.persister.FetchGS(cmd.GameID)
		if err != nil {
			d.errorLogger.ErrorLog(ctx, "Error fetching game state from persistence: %v", zap.Error(err))
		}

		// extract the moveData from the command
		moveData := cmd.DeclaredMove
		moveParts := strings.Split(moveData, ".")
		new_key, _ := strconv.Atoi(moveParts[0])
		new_val := moveParts[2]

		validMove := true
		// first we check if the list of all moves contains already a move with the same moveCounter (ie the 25th tile is already set)
		if _, ok := acceptedMoves.list[new_key]; ok {
			d.errorLogger.InfoLog(ctx, "This moveCounter value is already present in the AcceptedMoves list", zap.String("DuplicateMoveNumber", moveData))
			validMove = false
		} else {
			// second, we check if the unique moveCounter cooresponds to a unique coordinate
			reversedMap := reverseMap(acceptedMoves.list)
			if _, found := reversedMap[new_val]; found {
				d.errorLogger.InfoLog(ctx, "This xy coord pair is already present in the AcceptedMoves list", zap.String("DuplicatePosition", moveData))
				validMove = false
			}
		}

		if !validMove {
			newEvent := &InvalidMoveEvent{
				GameID:      cmd.GameID,
				PlayerID:    cmd.SourcePlayerID,
				InvalidMove: cmd.DeclaredMove,
			}
			d.EventDispatcher(newEvent)

		} else {
			newEvent := &OfficialMoveEvent{
				GameID:    cmd.GameID,
				PlayerID:  cmd.SourcePlayerID,
				MoveData:  cmd.DeclaredMove,
				Timestamp: time.Now(),
				AcceptedMoves: &AcceptedMoves.list,
			}

			d.EventDispatcher(newEvent)
		}
	case *PlayerForfeitingCmd:

		d.eventCmdLogger.Info(ctx, "Player forfeited the game", zap.String("PlayerID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))

		d.timer.StopTimer()

		// Determine who the winner is via the allLockedGames struct and the PlayerForfeitingCmd
		lg := allLockedGames[cmd.GameID]
		var newEvt *GameEndsEvent

		if cmd.SourcePlayerID == lg.Player1.PlayerID {
			newEvt = &GameEndsEvent{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: "Forfeit: " + cmd.Reason,
			}
		} else if cmd.SourcePlayerID == lg.Player2.PlayerID {
			newEvt = &GameEndsEvent{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: "Forfeit: " + cmd.Reason,
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

		d.timer = MakeNewTimer()
		d.timer.startChan <- struct{}{}

	case *CheckForWinConditionCmd:

		d.persister.FetchGS(cmd.GameID)



		newCmd := &CheckForWinConditionCmd{
			GameID:    event.GameID,
			MoveCount: event.MoveCounter,
			PlayerID:  event.PlayerID,
			Timestamp: time.Now()



	

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
		GameID:      gameID,
		PlayerID:    playerID,
		MoveCounter: moveCounter,
		xCoordinate: xCoordinate,
		yCoordinate: yCoordinate,
		ConcatenatedMove: coordinates,
	}
	return update, nil
}

func (d *Dispatcher) WrappedHandlerforEachlMoveEvent(ctx context.Context, evt *OfficialMoveEvent) *GameStateUpdate {

	newEvt, err := d.parseOfficialMoveEvt(ctx, evt)
	if err != nil {
		d.errorLogger.ErrorLog(ctx, "error extracting move data", zap.Error(err))
	}
	return newEvt
}

func reverseMap(m map[int]string) map[string]int {
	n := make(map[string]int, len(m))
	for k, v := range m {
		n[v] = k
	}
	return n
}
