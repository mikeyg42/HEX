package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	watermill "github.com/ThreeDotsLabs/watermill"
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
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
	"DeclaredMoveEvent": &DeclaredMoveEvent{},
	"InvalidMoveEvent":  &InvalidMoveEvent{},
	"OfficialMoveEvent": &OfficialMoveEvent{},
	"GameEndsEvent":     &GameEndsEvent{},
	"TimerEvent":        &TimerEvent{},
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
	GameID   string `json:"gameId"`
	PlayerID string `json:"playerId"`
	MoveData string `json:"moveData"`
}

// this includes the "start of game" event which will be effectively official move #0
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

//................ commands ......................//

var cmdTypeMap = map[string]interface{}{
	"DeclaringMoveCmd":           &DeclaringMoveCmd{},
	"StartingGameCmd":            &StartingGameCmd{},
	"EvaluatingMoveValidityCmd":  &EvaluatingMoveValidityCmd{},
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
type StartingGameCmd struct {
	Command
	ID        string    `json:"id"`
	GameID    string    `json:"gameId"`
	Timestamp time.Time `json:"timestamp"`
}

type EvaluatingMoveValidityCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	ValidMove      bool      `json:"validMove"`
	Timestamp      time.Time `json:"timestamp"`
}

type PlayerForfeitingCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	Timestamp      time.Time `json:"timestamp"`
}

type UpdatingGameStateUpdateCmd struct {
	Command
	ID             string    `json:"id"`
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	Timestamp      time.Time `json:"timestamp"`
}

//..............dispatcher ..................//

// Dispatcher represents the combined event and command dispatcher.
type Dispatcher struct {
	commandChan     chan interface{}
	eventChan       chan interface{}
	client          *redis.Client
	errorLogger     Logger
	commandHandlers map[string]func(interface{})
	eventHandlers   map[string]func(interface{})
}

//................................................//
//................... MAIN .......................//
//................................................//

func main() {
	eventCmdLogger := initLogger("/Users/mikeglendinning/projects/HEX/eventCommandLog.log", gormlogger.Info)
	errorLogger := initLogger("/Users/mikeglendinning/projects/HEX/errorLog.log", gormlogger.Info)

	client, err := getRedisClient()
	if err != nil {
		errorLogger.ErrorLog("Error connecting to Redis:", err)
		return
	}

	// Initialize command and event dispatchers
	d := NewDispatcher(client, errorLogger)

	// Start the dispatchers
	d.Start()

	// Example of dispatching a command and publishing an event
	cmd := &DeclaringMoveCmd{
		GameID:         "your_game_id",
		SourcePlayerID: "player1",
		DeclaredMove:   "4.P1.B5",
		Timestamp:      time.Now(),
	}
	logCommand(cmd, eventCmdLogger)
	d.CommandDispatcher(cmd)

	event := &InvalidMoveEvent{
		GameID:   "your_game_id",
		PlayerID: "player1",
		MoveData: "4.P1.B5",
	}
	logEvent(event, eventCmdLogger)
	d.EventDispatcher(event)

	timerEvent := &TimerEvent{
		GameID:    "your_game_id",
		PlayerID:  "your_player_id",
		EventType: "timer_expired",
		Timestamp: time.Now(),
	}
	time.AfterFunc(30*time.Second, func() {
		d.EventDispatcher(timerEvent)
	})

	time.Sleep(5 * time.Second) // Allow some time for the handlers to process the messages

	// Close the Redis client
	err = client.Close()
	if err != nil {
		errorLogger.ErrorLog("Error closing Redis client:", err)
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

	pong, err := client.Ping(context.Background()).Result()
	fmt.Println(pong, err)

	return client, err

}

// NewDispatcher creates a new Dispatcher instance.
func NewDispatcher(client *redis.Client, errorLogger Logger) *Dispatcher {
	dispatcher := &Dispatcher{
		commandChan: make(chan interface{}),
		eventChan:   make(chan interface{}),
		client:      client,
		errorLogger: errorLogger,
	}

	done := make(chan struct{})

	// Subscribe to Redis Pub/Sub channels for commands and events
	dispatcher.subscribeToCommands(done)
	dispatcher.subscribeToEvents(done)

	return dispatcher

}

func (d *Dispatcher) initializeDb(eventCmdLogger Logger) {
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
		d.errorLogger.ErrorLog("Error connecting to database:", err)
		return
	}

	// models.Player struct is automatically migrated to the database
	db.AutoMigrate(&GameStateUpdate{})

	eventCmdLogger.InfoLog("connected to database", nil)

	// declare DB as a Dbinstance, and thus a global
	DB = Dbinstance{Db: db}
}

//................................................//
//................. PERSISTANCE ..................//
//................................................//

type Event interface {
	// ModifyPersistedDataTable will be used by events to persist data to the database.
	ModifyPersistedDataTable() error
}

type Command interface{}

type Dbinstance struct {
	Db *gorm.DB
}

var DB Dbinstance

type GameStateUpdate struct {
	gorm.Model
	GameID      string `gorm:"index"`
	MoveCounter int
	PlayerID    string `gorm:"index"`
	XCoordinate string
	YCoordinate int
}

// For OfficialMoveEvent
func (e *OfficialMoveEvent) ModifyPersistedDataTable() error {

	moveParts := strings.Split(e.MoveData, ".")
	if len(moveParts) != 3 {
		return fmt.Errorf("invalid moveData format")
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

	YCoordinateInt, err := strconv.Atoi(yCoordinate)
	if err != nil {
		return err
	}

	GameStateUpdate := GameStateUpdate{
		GameID:      e.GameID,
		MoveCounter: moveCounterInt,
		PlayerID:    playerID,
		XCoordinate: xCoordinate,
		YCoordinate: YCoordinateInt,
	}

	result := DB.Db.Create(&GameStateUpdate)

	if result.RowsAffected != 1 {
		fmt.Println("number of rows affected by DB update was not 1, but instead %v", result.RowsAffected)
	}

	if result.Error != nil {
		fmt.Println("failed to update DB", result.Error)
		return result.Error
	}

	return nil
}

// For GameEndsEvent
func (e *GameEndsEvent) ModifyPersistedDataTable() error {

	GameStateUpdate := &GameStateUpdate{
		GameID:      e.GameID,
		MoveCounter: -99,
		PlayerID:    e.WinnerID + " WINS",
		XCoordinate: e.WinCondition,
		YCoordinate: 0,
	}

	err := DB.Db.Create(GameStateUpdate).Error
	if err != nil {
		return err
	}

	return nil
}

// log the event or command in a generic way using reflection.
func logEvent(event interface{}, logger Logger) {
	eventType := reflect.TypeOf(event).Name()
	logger.InfoLog("Received "+eventType, zap.Any("event", event))

}

func logCommand(cmd interface{}, logger Logger) {
	cmdType := reflect.TypeOf(cmd).Name()
	logger.InfoLog("Received "+cmdType, zap.Any("command", cmd))
}

//................................................//
//................DISPATCHERS.....................//
//................................................//

// Start starts the command and event dispatchers.
func (d *Dispatcher) Start() {
	go d.commandDispatcher()
	go d.eventDispatcher()
}

// CommandDispatcher is FIRST STOP for a message in this channel - it dispatches the command to the appropriate channel.
func (d *Dispatcher) CommandDispatcher(cmd interface{}) {
	d.commandChan <- cmd
}

func (d *Dispatcher) commandDispatcher() {
	for cmd := range d.commandChan {
		payload, err := json.Marshal(cmd)
		if err != nil {
			d.errorLogger.ErrorLog("Error marshaling command", err)
			continue
		}

		// Publish command to Redis Pub/Sub channel
		err = d.client.Publish(context.Background(), "commands", payload).Err()
		if err != nil {
			d.errorLogger.ErrorLog("Error Publishing command", err)
		}
	}
}

// EventDispatcher dispatches the event to the appropriate channel.
func (d *Dispatcher) EventDispatcher(event interface{}) {
	d.eventChan <- event
}

func (d *Dispatcher) eventDispatcher() {
	for event := range d.eventChan {
		data, err := json.Marshal(event)
		if err != nil {
			d.errorLogger.ErrorLog("Error marshaling event", err)
			continue
		}

		// Publish event to Redis Pub/Sub channel
		err = d.client.Publish(context.Background(), "events", data).Err()
		if err != nil {
			d.errorLogger.ErrorLog("Error Publishing event", err)
		}
	}
}

// Subscribe to commands and events with the done channel
func (d *Dispatcher) subscribeToCommands(done <-chan struct{}) {
	commandPubSub := d.client.Subscribe(context.Background(), "commands")

	// Command receiver/handler
	go func() {
		cmdCh := commandPubSub.Channel()
		for {
			select {
			case <-done:
				commandPubSub.Close()
				return
			case msg := <-cmdCh:
				var cmd Command
				cmdType, found := cmdTypeMap[msg.Channel]
				if !found {
					d.errorLogger.InfoLog("unknown command type", msg.Channel)
					continue
				}

				cmdValue := reflect.New(reflect.TypeOf(cmdType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &cmdValue)
				if err != nil {
					d.errorLogger.ErrorLog(fmt.Sprintf("Error unmarshaling %s: %v\n", msg.Channel), err)
					continue
				}

				cmd = cmdValue
				d.handleCommand(done, cmd, commandPubSub)
			}
		}
	}()
}

func (d *Dispatcher) subscribeToEvents(done <-chan struct{}) {
	eventPubSub := d.client.Subscribe(context.Background(), "events")
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
				var event Event
				eventType, found := eventTypeMap[msg.Channel]
				if !found {
					d.errorLogger.InfoLog("unknown event type", msg.Channel)
					continue
				}

				eventValue := reflect.New(reflect.TypeOf(eventType).Elem()).Interface()
				err := json.Unmarshal([]byte(msg.Payload), &eventValue)
				if err != nil {
					d.errorLogger.ErrorLog(fmt.Sprintf("Error unmarshaling %s: %v\n", msg.Channel), err)
					continue
				}
				event, ok := eventValue.(Event)
				if !ok {
					d.errorLogger.InfoLog("unmarshaled event does not implement Event interface", nil)
					continue
				}

				d.handleEvent(done, event, eventPubSub)
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
func (d *Dispatcher) handleEvent(done <-chan struct{}, event Event, eventPubSub *redis.PubSub) {
	// define the context and cancel function for the countdown handler
	var cancelChan = make(chan struct{})

	switch event := event.(type) {

	case *DeclaredMoveEvent:
		// Handle declared move event
		fmt.Println("Received DeclaredMoveEvent:", event)
	case *InvalidMoveEvent:

		// Handle invalid move event
		fmt.Println("Received InvalidMoveEvent:", event)

	case *OfficialMoveEvent:
		// stop the timer as soon as this signal is received to prevent the timer from expiring when it shouldn't
		cancelChan <- struct{}{}

		// Persist the accepted move to a database
		err := event.ModifyPersistedDataTable()
		if err != nil {
			fmt.Println("Error persisting official move:", err)
		}

		// Create a new context for the countdown handler.
		ctx, cancel := context.WithCancel(context.Background())

		// Start the countdown handler as a goroutine.
		go func() {
			err := CountdownHandler(ctx, event)
			if err != nil {
				fmt.Println("Error handling event:", err)
			}
		}()

		// Create a channel to receive the signal to cancel the countdown handler.
		cancelChan := make(chan struct{})

		// Start a goroutine to wait for the cancel signal and then cancel the countdown handler.
		go func() {
			// Wait for the cancel signal from another goroutine, e.g., the move handler.
			<-cancelChan

			// Cancel the context, which will stop the countdown handler.
			cancel()
		}()

	case *GameEndsEvent:

		// Handle game ends event
		fmt.Println("Received GameEndsEvent:", event)

		// Persist the game end to a database
		err := event.ModifyPersistedDataTable()
		if err != nil {
			fmt.Println("Error persisting games end move:", err)
		}

		eventPubSub.Close()

	case *TimerEvent:

		// Handle timer event
		fmt.Println("Received TimerEvent:", event)

	default:
		d.errorLogger.Error("Unknown event type", nil, watermill.LogFields{"event": event})

		fmt.Println("Unknown event type")
	}
}

// CountdownHandler is the event handler that starts a countdown clock when a valid move is published.
func CountdownHandler(ctx context.Context, event Event) error {
	fmt.Printf("Received move from Player %s: %s\n", event.PlayerID, event.MoveData)

	// Start the 30-second countdown clock.
	timer := time.NewTimer(moveTimeout)

	// Wait for either the countdown to expire or a new move event.
	select {
	case <-timer.C:
		fmt.Printf("Player %s has exceeded the time limit and forfeits the game.\n", event.PlayerID)
		// Perform actions for game-ending event due to timeout.

	case <-ctx.Done():
		// The context is canceled if a new move event is received.
		timer.Stop()
		fmt.Printf("Player %s has made a new move before the timeout.\n", event.PlayerID)
		// Reset the countdown clock or perform any other actions for valid moves.
	}

	return nil
}

//................................................//
//................................................//

type AcceptedMoves struct {
	list map[int]string // key is 1, 2, 3, 4,... Value is "a1", "b2", "h1", etc.
}

// populateAcceptedMoves_fromDB fetches data from the PostgreSQL database and populates the AcceptedMoves struct.
func (d *Dispatcher) populateAcceptedMoves_fromDB(gameID string) (*AcceptedMoves, error) {

	var gameStateUpdates []GameStateUpdate
	acceptedMoves := AcceptedMoves{
		list: make(map[int]string),
	}

	// Step 1: Sort by gameID and extract relevant columns
	if err := DB.Db.Table("game_state_updates").
		Select("move_counter, x_coordinate, y_coordinate").
		Where("game_id = ?", gameID).
		Order("move_counter").
		Find(&gameStateUpdates).Error; err != nil {
		return nil, err
	}

	// Process the result to create the AcceptedMoves map
	for _, update := range gameStateUpdates {
		// Convert YCoordinate to a string and concatenate XCoordinate and YCoordinate to form the map's value
		yCoordinateStr := strconv.Itoa(update.YCoordinate)
		concatenatedMoveData := update.XCoordinate + yCoordinateStr

		// Use moveCounter as the key
		acceptedMoves.list[update.MoveCounter] = concatenatedMoveData
	}

	return &acceptedMoves, nil
}

// populateAcceptedMoves_fromCache fetches data from the Redis cache and populates the AcceptedMoves struct.
func (d *Dispatcher) populateAcceptedMoves_fromCache(gameID string) (*AcceptedMoves, error) {

	moveList := make(map[string]string)
	moveList, err := d.client.HGetAll(context.Background(), gameID).Result()
	if err != nil {
		d.errorLogger.ErrorLog("error extracting cache, resorting to use of DB", err)
		return d.populateAcceptedMoves_fromDB(gameID)
	}

	// Convert the result to a Go map
	allMoves := make(map[int]string, len(moveList))

	// convert string to int
	for field, value := range moveList {
		f, err := strconv.Atoi(field)
		if err != nil {
			d.errorLogger.ErrorLog("error converting cache hash to Go map", err)
			return d.populateAcceptedMoves_fromDB(gameID)
		}
		allMoves[f] = value
	}

	acceptedMoves := AcceptedMoves{
		list: allMoves,
	}

	return &acceptedMoves, nil
}

func reverseMap(m map[int]string) map[string]int {
	n := make(map[string]int, len(m))
	for k, v := range m {
		n[v] = k
	}
	return n
}

// Function to handle the command and validate the move
func (d *Dispatcher) handleCommand(done <-chan struct{}, cmd Command, commandPubSub *redis.PubSub) {
	switch cmd := cmd.(type) {
	case *DeclaringMoveCmd:
		key := "acceptedMoveList"

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

		newCmd := &EvaluatingMoveValidityCmd{
			ID:             uuid.New().String(),
			GameID:         cmd.GameID,
			SourcePlayerID: cmd.SourcePlayerID,
			DeclaredMove:   cmd.DeclaredMove,
			ValidMove:      validMove,
			Timestamp:      time.Now(),
		}

		d.commandChan <- newCmd

	case *PublishingMoveCmd:
		// Handle PublishMove command
		fmt.Println("Received PublishingMoveCmd:", cmd)
	case *StartingGameCmd:
		// Handle StartGame command
		fmt.Println("Received StartingGameCmd:", cmd)
	case *EvaluatingMoveValidityCmd:
		// Handle EvaluateMoveValidity command
		fmt.Println("Received EvaluatingMoveValidityCmd:", cmd)

	case *PlayerForfeitingCmd:
		// Handle Player Forfeiting command
		fmt.Println("Received PlayerForfeitingCmd:", cmd)
	case *UpdatingGameStateUpdateCmd:
		// Handle Update Game State command
		fmt.Println("Received UpdatingGameStateUpdateCmd:", cmd)

	default:
		d.errorLogger.ErrorLog(fmt.Sprintf("Unknown command type %w", cmd), nil)

		fmt.Println("Unknown command type")
	}
}

func (d *Dispatcher) getCacheSize(key string) (int, error) {

	//numElements, err := d.client.HLen(context.Background(), key).Result()
	allKeys, err := (d.client.HKeys(context.Background(), key)).Result()
	if err != nil {
		d.errorLogger.ErrorLog("error querying caches key list", err)
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
		d.errorLogger.ErrorLog("error querying cache", fmt.Errorf("numElements != maxMoveCounter"))
		return -1, nil
	}

	return maxCounter, nil
}
