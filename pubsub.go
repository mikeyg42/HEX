package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	redis "github.com/redis/go-redis/v9"

	"github.com/google/uuid"
	zap "go.uber.org/zap"
	"gorm.io/driver/postgres"
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
	"GameEndsEvent":         &GameEndsEvent{},
	"GameStartEvent":        &GameStartEvent{},
	"TimerEvent":            &TimerEvent{},
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

type GameEndsEvent struct {
	Event
	GameID       string `json:"gameId"`
	WinnerID     string `json:"winnerId"`
	LoserID      string `json:"loserId"`
	WinCondition string `json:"moveData"`
}

type TimerEvent struct {
	Event
	GameID    string    `json:"gameId"`
	PlayerID  string    `json:"playerId"`
	EventType string    `json:"eventType"` // This will help distinguish the timer event
	Timestamp time.Time `json:"timestamp"`
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
var lockedGames = make(map[string]LockedGame)
var lockMutex sync.Mutex

//................ commands ......................//

var cmdTypeMap = map[string]interface{}{
	"DeclaringMoveCmd":           &DeclaringMoveCmd{},
	"LetsStartTheGameCmd":        &LetsStartTheGameCmd{},
	"PlayerForfeitingCmd":        &PlayerForfeitingCmd{},
	"UpdatingGameStateUpdateCmd": &UpdatingGameStateUpdateCmd{},
}

type DeclaringMoveCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	Timestamp      time.Time `json:"timestamp"`
}

// maybe should be  an event
type LetsStartTheGameCmd struct {
	Command
	GameID    string    `json:"gameId"`
	Player1   string    `json:"player1"`
	Player2   string    `json:"player2"`
	Timestamp time.Time `json:"timestamp"`
}

type PlayerForfeitingCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	Timestamp      time.Time `json:"timestamp"`
	Reason         string    `json:"reason"`
}

type UpdatingGameStateUpdateCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	Timestamp      time.Time `json:"timestamp"`
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
}

type Worker struct {
	WorkerID      string
	GameID        string
	PlayerChan    chan PlayerIdentity
	ReleasePlayer chan string // Channel to notify the worker to release the player
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

	client, err := getRedisClient()
	if err != nil {
		errorLogger.ErrorLog(ctx, "Error getting Redis Client", zap.Bool("get redisClient", false))
		return
	}

	// Initialize command and event dispatchers
	d := NewDispatcher(client, errorLogger, eventCmdLogger)

	// Initialize database
	err = d.initializeDb()
	if err != nil {
		errorLogger.ErrorLog(ctx, "Error initializing the database:", zap.Error(err))
		return
	}

	// Starts the command and event dispatchers's goroutines
	d.Start(ctx)

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
func getRedisClient() (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // Redis password if required
		DB:       0,  // Default DB
	})

	_, err := client.Ping(context.Background()).Result()

	return client, err

}

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(client *redis.Client, errorLogger Logger, eventCmdLogger Logger) *Dispatcher {
	dispatch := &Dispatcher{
		commandChan:    make(chan interface{}),
		eventChan:      make(chan interface{}),
		client:         client,
		errorLogger:    errorLogger,
		eventCmdLogger: eventCmdLogger,
	}

	//  the DONE chan controls the Dispatcher! closing it will stop the dispatcher and both pubsubs
	done := make(chan struct{})

	// Subscribe to Redis Pub/Sub channels for commands and events
	dispatch.subscribeToCommands(done)
	dispatch.subscribeToEvents(done)

	return dispatch

}

func (d *Dispatcher) initializeDb() error {
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
		Logger: d.errorLogger,
	})

	if err != nil {
		return err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return err
	}

	sqlDB.SetMaxIdleConns(20)
	sqlDB.SetMaxOpenConns(200)

	// models.Player struct is automatically migrated to the database
	db.AutoMigrate(&GameStateUpdate{})

	// declare DB as a Dbinstance, and thus a global
	DB = Dbinstance{Db: db}

	return nil
}

//................................................//
//................. PERSISTANCE ..................//
//................................................//

type Event interface {
	// ModifyPersistedDataTable will be used by events to persist data to the database.
	ModifyPersistedDataTable(ctx context.Context) error
	MarshalEvt() ([]byte, error)
}

type Command interface {
	MarshalCmd() ([]byte, error)
}

type Dbinstance struct {
	Db *gorm.DB
}

var DB Dbinstance

type GameStateUpdate struct {
	gorm.Model
	GameID      string `gorm:"index"`
	MoveCounter int
	PlayerID    string `gorm:"index"`
	xCoordinate string
	yCoordinate int
}

// For OfficialMoveEvent
func (e *OfficialMoveEvent) ModifyPersistedDataTable(ctx context.Context) error {
	logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
	if !ok {
		return fmt.Errorf("failed to get logger from context")
	}

	moveParts := strings.Split(e.MoveData, ".")
	if len(moveParts) != 3 {
		logger.ErrorLog(ctx, "invalid moveData format, != 3 parts",
			zap.String("gameID", e.GameID),
			zap.String("moveData", e.MoveData),
		)
		return fmt.Errorf("invalid moveData format, != 3 parts")
	}

	moveCounter := moveParts[0]
	playerID := moveParts[1]
	coordinates := moveParts[2]

	xCoordinate := coordinates[:1]
	yCoordinate := coordinates[1:]

	moveCounterInt, err := strconv.Atoi(moveCounter)
	if err != nil {
		return err
	}

	yCoordinateInt, err := strconv.Atoi(yCoordinate)
	if err != nil {
		return err
	}

	newUpdate := GameStateUpdate{
		GameID:      e.GameID,
		MoveCounter: moveCounterInt,
		PlayerID:    playerID,
		xCoordinate: xCoordinate,
		yCoordinate: yCoordinateInt,
	}

	result := DB.Db.Create(&newUpdate)

	if result.RowsAffected != 1 {
		logger.ErrorLog(ctx, "number of rows affected by DB update was != 1",
			zap.String("gameID", e.GameID),
			zap.String("moveData", e.MoveData),
			zap.Int64("rowsAffected", result.RowsAffected),
		)
		return fmt.Errorf("number of rows affected by DB update was != 1 ")
	}

	if result.Error != nil {
		logger.ErrorLog(ctx, "failed to update DB", zap.Error(result.Error))
		return result.Error
	}

	return nil
}

// For GameEndsEvent
func (e *GameEndsEvent) ModifyPersistedDataTable(ctx context.Context) error {
	logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
	if !ok {
		return fmt.Errorf("failed to get logger from context")
	}

	GameStateUpdate := &GameStateUpdate{
		GameID:      e.GameID,
		MoveCounter: -99,
		PlayerID:    e.WinnerID + " WINS",
		xCoordinate: e.WinCondition,
		yCoordinate: 0,
	}

	err := DB.Db.Create(GameStateUpdate).Error
	if err != nil {
		logger.ErrorLog(ctx, "failed to update DB",
			zap.String("gameID", e.GameID),
			zap.Error(err),
		)
		return err
	}

	return nil
}

//................................................//
//................DISPATCHERS.....................//
//................................................//

// Start starts the command and event dispatchers.
func (d *Dispatcher) Start(ctx context.Context) {
	errChan := make(chan error) // create an error channel

	go d.commandDispatcher(ctx, errChan)
	go d.eventDispatcher(ctx, errChan)

	// Let's listen to our error channel now
	go func() {
		for err := range errChan {
			// Log the error or handle it appropriately.
			logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
			if ok {
				logger.ErrorLog(ctx, "Error in dispatcher", zap.Error(err))
			}
		}
	}()
}

// CommandDispatcher is FIRST STOP for a message in this channel - it dispatches the command to the appropriate channel.
func (d *Dispatcher) CommandDispatcher(cmd interface{}) {
	d.commandChan <- cmd
}

func (d *Dispatcher) commandDispatcher(ctx context.Context, errChan chan<- error) {
	logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
	if !ok {
		errChan <- fmt.Errorf("failed to get logger from context in commandDispatcher")
	}

	for cmd := range d.commandChan {
		payload, err := json.Marshal(cmd)
		if err != nil {
			logger.ErrorLog(ctx, "Error marshaling command", zap.Error(err))
			errChan <- err
		}

		// Publish command to Redis Pub/Sub channel
		err = d.client.Publish(ctx, "commands", payload).Err()
		if err != nil {
			logger.ErrorLog(ctx, "Error Publishing command", zap.Error(err))
			errChan <- err
		}
	}
}

// EventDispatcher dispatches the event to the appropriate channel.
func (d *Dispatcher) EventDispatcher(event interface{}) {
	d.eventChan <- event
}

func (d *Dispatcher) eventDispatcher(ctx context.Context, errChan chan<- error) {
	logger, ok := ctx.Value(errorLoggerKey{}).(Logger)
	if !ok {
		errChan <- fmt.Errorf("failed to get logger from context in eventDispatcher")
	}

	for event := range d.eventChan {

		payload, err := json.Marshal(event)
		if err != nil {
			logger.ErrorLog(ctx, "Error marshaling event (eventDispatcher)", zap.Error(err))
			errChan <- err
		}

		// Publish event to Redis Pub/Sub channel
		err = d.client.Publish(ctx, "events", payload).Err()
		if err != nil {
			logger.ErrorLog(ctx, "Error Publishing event (eventDispatcher)", zap.Error(err))
			errChan <- err
		}
	}
}

// Subscribe to commands and events with the done channel
// subscribeToCommands with error channel
func (d *Dispatcher) subscribeToCommands(done <-chan struct{}, errChan chan<- error) {
	ctx := context.Background()
	commandPubSub := d.client.Subscribe(ctx, "commands")

	// Command receiver/handler
	go func() {
		cmdCh := commandPubSub.Channel()
		for {
			select {
			case <-done:
				commandPubSub.Close()
				return
			case msg := <-cmdCh:
				cmdType, found := cmdTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown command type: %s", cmdType.(string))
					d.errorLogger.InfoLog(ctx, "unknown command type", zap.String("cmdType", cmdType.(string)))
					errChan <- err
					continue
				}

				cmdValue := reflect.New(reflect.TypeOf(cmdType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &cmdValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.errorLogger.ErrorLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}
				// Type assertion to ensure that the unmarshaled event implements the Command interface
				cmdTypeAsserted, ok := cmdValue.(Command)
				if !ok {
					err := fmt.Errorf("unmarshaled command does not implement Event interface")
					d.errorLogger.InfoLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}
				d.handleCommand(done, cmdTypeAsserted, commandPubSub)
			}
		}
	}()
}

// subscribeToEvents with error channel
func (d *Dispatcher) subscribeToEvents(done <-chan struct{}, errChan chan<- error) {
	ctx := context.Background()
	eventPubSub := d.client.Subscribe(ctx, "events")
	defer eventPubSub.Close()

	// Event handler
	go func() {
		eventCh := eventPubSub.Channel()
		for {
			select {
			case <-done:
				eventPubSub.Close()
				return
			case msg := <-eventCh:
				eventType, found := eventTypeMap[msg.Channel]
				if !found {
					err := fmt.Errorf("unknown event type: %s", msg.Channel)
					d.errorLogger.InfoLog(ctx, err.Error())
					errChan <- err
					continue
				}

				eventValue := reflect.New(reflect.TypeOf(eventType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &eventValue)
				if err != nil {
					err = fmt.Errorf("Error unmarshaling %s: %v\n", msg.Channel, err)
					d.errorLogger.ErrorLog(ctx, err.Error(), zap.Error(err))
					errChan <- err
					continue
				}
				// Type assertion to ensure that the unmarshaled event implements the Event interface
				evtTypeAsserted, ok := eventValue.(Event)
				if !ok {
					err := fmt.Errorf("unmarshaled event does not implement Event interface")
					d.errorLogger.InfoLog(ctx, err.Error(), zap.Error(nil))
					errChan <- err
					continue
				}

				d.handleEvent(ctx, evtTypeAsserted, eventPubSub)
			}
		}
	}()
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
	// Create a new context for the countdown handler with a deadline.
	ctxCountdown, cancelCountdown := context.WithTimeout(ctx, moveTimeout)
	defer cancelCountdown()

	// Create a channel to receive the signal to cancel the countdown handler.
	cancelChan := make(chan struct{})
	defer close(cancelChan)

	switch event := event.(type) {
	case *InvalidMoveEvent:
		d.errorLogger.Warn(ctx, "Received InvalidMoveEvent: %v", event)
	case *OfficialMoveEvent:
		d.eventCmdLogger.Info(ctx, event.GameID+"/"+event.PlayerID+" has made a move: "+event.MoveData)

		// Persist the accepted move to a database
		if err := event.ModifyPersistedDataTable(ctx); err != nil {
			return fmt.Errorf("error persisting official move: %w", err)
		}

		// Start the countdown handler as a goroutine.
		go func() {
			timer := time.NewTimer(moveTimeout)

			if err := CountdownHandler(ctxCountdown, event, timer); err != nil {
				d.errorLogger.Error(ctx, "Error handling event: %v", err)
			}
			// Cancel the context, which will stop the countdown handler.
			cancelChan <- struct{}{}
		}()

	case *GameEndsEvent:
		d.eventCmdLogger.Info(ctx, event.GameID+"/"+event.PlayerID+" has made a game ending Move!: "+event.MoveData)

		// ends the timer !
		cancelChan <- struct{}{}

		// Persist the game end to a database
		err := event.ModifyPersistedDataTable()
		if err != nil {
			d.errorLogger.Error(ctx, "Error persisting games end move: %v", err)
		}
		eventPubSub.Close()

	case *TimerEvent:
		d.eventCmdLogger.Info(ctx, "Received TimerEvent: %v", event)

		timeNow := time.Now()
		timeElapsed := timeNow.Sub(event.Timestamp)
		if timeElapsed < time.Second*35 {
			d.errorLogger.Error(ctx, "timer expired prematurely after %v seconds", timeElapsed.Seconds())
		}

		newCmd := &PlayerForfeitingCmd{
			GameID:         event.GameID,
			SourcePlayerID: event.PlayerID,
			Timestamp:      event.Timestamp,
			Reason:         fmt.Sprintf("Timer:%v", timeElapsed),
		}

		d.CommandDispatcher(newCmd)

	default:
		d.errorLogger.Error(ctx, "Unknown event type: %v", fmt.Errorf("Weird event type: %T", event))
		d.eventCmdLogger.Info(ctx, "Received Unusual, unknown event: %v", event)
	}
	return nil
}

func CountdownHandler(ctx context.Context, event Event, timer *time.Timer) error {

	// Wait for either the countdown to expire or a new move event.
	select {
	case <-timer.C:
		fmt.Printf("Player %s has exceeded the time limit and forfeits the game.\n", event.PlayerID)
		// Perform actions for game-ending event due to timeout.
		return fmt.Errorf("Player %s has exceeded the time limit and forfeits the game", event.PlayerID)

	case <-ctx.Done():
		// The context is canceled if a new move event is received.
		timer.Stop()
		fmt.Printf("Player %s has made a new move before the timeout.\n", event.PlayerID)
		// Reset the countdown clock or perform any other actions for valid moves.
	}

	return nil
}

func (d *Dispatcher) handleCommand(done <-chan struct{}, cmd Command, commandPubSub *redis.PubSub) {
	switch cmd := cmd.(type) {
	case *DeclaringMoveCmd:
		key := "acceptedMoveList" // key is for accessing the cache

		numCache, err := d.getCacheSize(key)
		if err != nil {
			d.errorLogger.ErrorLog("error querying cache", err)
		}

		var acceptedMoves *AcceptedMoves

		if numCache > 0 {
			// then we can use the cache!
			acceptedMoves, err = d.populateAcceptedMoves_fromCache(cmd.GameID)
			if err != nil {
				d.errorLogger.ErrorLog("error fetching accepted Move list! ", err)
			}
		} else if numCache < 0 { // cache is no bueno, use the DB
			acceptedMoves, err := d.populateAcceptedMoves_fromDB(cmd.GameID)
			if err != nil {
				d.errorLogger.ErrorLog("error fetching accepted Move list! ", err)
			}
			d.client.HSet(context.Background(), key, acceptedMoves.list)
		}

		// extract the moveData from the command
		moveData := cmd.DeclaredMove
		moveParts := strings.Split(moveData, ".")
		new_key, _ := strconv.Atoi(moveParts[0])
		new_val := moveParts[2]

		validMove := true
		// first we check if the list of all moves contains already a move with the same moveCounter (ie the 25th tile is already set)
		if _, ok := acceptedMoves.list[new_key]; ok {
			d.errorLogger.InfoLog("This moveCounter value is already present in the AcceptedMoves list", moveData)
			validMove = false
		} else {
			// second, we check if the unique moveCounter cooresponds to a unique coordinate
			reversedMap := reverseMap(acceptedMoves.list)
			if _, found := reversedMap[new_val]; found {
				d.errorLogger.InfoLog("This xy coord pair is already present in the AcceptedMoves list", moveData)
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
			}
			d.EventDispatcher(newEvent)
		}

	case *LetsStartTheGameCmd:

		// Create a new channel for this game instance
		gameID := cmd.GameID

		// Check if the game is still locked (not claimed by another goroutine)
		lockMutex.Lock()
		lockedGame, ok := lockedGames[gameID]
		delete(lockedGames, gameID) // Remove the game from the map
		lockMutex.Unlock()

		if !ok {
			// The game was claimed by another goroutine, abort this instance
			return
		}

		// Announce the game with the locked players in a new goroutine
		go lockGame(d, lockedGame)
		acked := checkForPlayerAck(cmd, lockedGame)
		if acked == false {
			return
		} else {
			startEvent := &GameStartEvent{
				GameID:         lockedGame.GameID,
				FirstPlayerID:  lockedGame.Player1.ID,
				SecondPlayerID: lockedGame.Player2.ID,
				Timestamp:      time.Now(),
			}

			d.EventDispatcher(startEvent)
		}

	}
}

func reverseMap(m map[int]string) map[string]int {
	n := make(map[string]int, len(m))
	for k, v := range m {
		n[v] = k
	}
	return n
}

func checkForPlayerAck(cmd *LetsStartTheGameCmd, lockedGame LockedGame) bool {
	acked := false

	// Wait for player1's acknowledgement
	time.Sleep(5 * time.Second)
	if isGameAcknowledged(cmd.GameID, lockedGame.Player1.PlayerID) {
		// Player1 acknowledged, proceed to wait for player2's acknowledgement
		time.Sleep(5 * time.Second)
		if isGameAcknowledged(cmd.GameID, lockedGame.Player2.PlayerID) {
			acked = true
		}
	}
	if acked == false {
		lockMutex.Lock()
		delete(lockedGames, cmd.GameID) // Remove the game from the map
		lockMutex.Unlock()
	}

	return acked
}

// NewWorker function with WorkerID assignment
func NewWorker() *Worker {
	// Generate a unique worker ID (e.g., using UUID)
	workerID := uuid.New().String()

	return &Worker{
		WorkerID:      workerID,
		PlayerChan:    make(chan PlayerIdentity),
		ReleasePlayer: make(chan string), // Initialize the broadcast channel
	}
}

// Define the acknowledgment map and mutex
var (
	acknowledgments = make(map[string][]string)
	ackMutex        = sync.Mutex{}
)

// Function to check if both players have acknowledged the game
func isGameAcknowledged(gameID, playerID string) bool {
	ackMutex.Lock()
	defer ackMutex.Unlock()

	// Check if the game is already in the acknowledgment map
	if acks, ok := acknowledgments[gameID]; ok {
		// Check if the player is not already in the acknowledgments
		for _, ackPlayerID := range acks {
			if ackPlayerID == playerID {
				// Player already acknowledged the game
				return true
			}
		}

		// Add the player to the acknowledgments for this game
		acknowledgments[gameID] = append(acks, playerID)

		// Check if both players have acknowledged the game
		if len(acknowledgments[gameID]) == 2 {
			// If both players acknowledged, delete the game entry from the map
			delete(acknowledgments, gameID)
			return true
		}
	} else {
		// If the game is not in the acknowledgments, create a new entry for it
		acknowledgments[gameID] = []string{playerID}
	}

	return false
}

func lockGame(d *Dispatcher, lockedGame LockedGame) {

	// Lock the game with the two players in the shared map
	lockMutex.Lock()
	_, ok := lockedGames[lockedGame.GameID]
	if !ok {
		lockedGames[lockedGame.GameID] = lockedGame
	}
	lockMutex.Unlock()

	// Check if the game is still locked (not claimed by another goroutine)
	if !ok {
		// The game was claimed by another goroutine, abort this instance
		return
	}

	// Wait for player1+2's acknowledgement
	time.Sleep(5 * time.Second)
	if isGameAcknowledged(lockedGame.GameID, lockedGame.Player1.PlayerID) {
		time.Sleep(5 * time.Second)
		if isGameAcknowledged(lockedGame.GameID, lockedGame.Player2.PlayerID) {
			// both acknowledged, start the game

			newEvent := &LetsStartTheGameCmd{
				GameID:    lockedGame.GameID,
				Player1:   lockedGame.Player1.PlayerID,
				Player2:   lockedGame.Player2.PlayerID,
				Timestamp: time.Now(),
			}
			d.EventDispatcher(newEvent)

			return
		}
	}
	// Player1 didn't acknowledge, abort the game
	lockMutex.Lock()
	delete(lockedGames, lockedGame.GameID) // Remove the game from the map
	lockMutex.Unlock()
	return
}

func (w *Worker) Run(d *Dispatcher) {
	for { // this is an infinite loop to continuously handle incoming players and games.
		// Listen for incoming player identities
		player1 := <-w.PlayerChan
		player2 := <-w.PlayerChan

		w.GameID = uuid.New().String() // Generate a unique game ID (e.g., using UUID)

		// Randomize player order with rand.Shuffle
		players := [2]PlayerIdentity{player1, player2}
		rand.Shuffle(2, func(i, j int) { players[i], players[j] = players[j], players[i] })

		// Lock the game with the two players in a shared map
		lockGame(d, LockedGame{
			GameID:   w.GameID,
			WorkerID: w.WorkerID,
			Player1:  players[0],
			Player2:  players[1],
		})

		// Wait for the release notification
		select {
		case playerID := <-w.ReleasePlayer:
			// Release the player if it matches any of the announced players
			if players[0].PlayerID == playerID || players[1].PlayerID == playerID {
				// Release the player and proceed to the next iteration
				continue
			}
		}
	}
}

func (d *Dispatcher) StartWorkerPool(numWorkers int) {
	// Create a new channel to announce games
	announceGameChan := make(chan *GameAnnouncementEvent)

	// Create and start a pool of workers
	workers := make([]*Worker, numWorkers)
	for i := 0; i < numWorkers; i++ {
		workers[i] = NewWorker()
		go workers[i].Run(d)
	}

	// players join  game chanvia the fronmt ewnd
	var gameChannel = make(chan PlayerIdentity)
	var playerCount int     // Counter to keep track of the number of players joined
	var lastWorkerIndex int // Keep track of the index of the last assigned worker

	lastWorkerIndex = 0
	playerCount = 0

	go func() {
		for {
			select {
			case player := <-gameChannel:
				// Use round-robin to select the next worker
				lastWorkerIndex = (lastWorkerIndex + 1) % numWorkers
				workers[lastWorkerIndex].PlayerChan <- player

				playerCount++
				if playerCount == 2 {
					// Close the channel to indicate that no more players should be added
					close(gameChannel)

					// the worker knows the gameID because it was set during "Run" function
					gameID := workers[lastWorkerIndex].GameID
					workerID := workers[lastWorkerIndex].WorkerID

					// Create a new channel to wait for player acknowledgements
					ackChannel := make(chan bool)

					// Announce the game with the locked players in a new goroutine
					go lockGame(d, gameID, workers[lastWorkerIndex].Player1, workers[lastWorkerIndex].Player2, ackChannel)

					// Wait for player acknowledgements
					for i := 0; i < 2; i++ {
						select {
						case <-ackChannel:
							// Player acknowledged, continue waiting for the other player
						case <-time.After(5 * time.Second):
							// Timeout if a player doesn't send acknowledgement within 5 seconds
							d.errorLogger.InfoLog("Timeout: Player didn't send acknowledgement within 5 seconds.")
							return
						}
					}

					// Both players acknowledged, start the game

					// send command to start the game!?!

				}
			case <-time.After(15 * time.Second):
				// Timeout if the second player doesn't send identity within 15 seconds.
				d.errorLogger.InfoLog("Timeout: Second player didn't send identity within 15 seconds.")
				close(gameChannel) // Close the channel to stop waiting for more players
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case announceEvent := <-announceGameChan:
				// Announce the game with the locked players
				d.EventDispatcher(announceEvent)

				// Notify all workers to release players that match the announced game's players
				for _, w := range workers {
					select {
					case w.ReleasePlayer <- announceEvent.FirstPlayerID:
					case w.ReleasePlayer <- announceEvent.SecondPlayerID:
					default:
						// If the worker's broadcast channel is busy, skip and proceed to the next worker
					}
				}
			}
		}
	}()
}
