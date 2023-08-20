package main

import(
gorm "gorm.io/gorm"
 "time"
 "fmt"
 "context"
 "strconv"
 "strings"
 "errors"
 cache "github.com/go-redis/cache/v9"
 redis "github.com/redis/go-redis/v9"
 zap "go.uber.org/zap"

)

//................ commands ......................//
type Event interface {
	// ModifyPersistedDataTable will be used by events to persist data to the database.
	ModifyPersistedDataTable(ctx context.Context) error
	MarshalEvt() ([]byte, error)
}

type Command interface {
	MarshalCmd() ([]byte, error)
}

var cmdTypeMap = map[string]interface{}{
	"DeclaringMoveCmd":    &DeclaringMoveCmd{},
	"LetsStartTheGameCmd": &LetsStartTheGameCmd{},
	"PlayerForfeitingCmd": &PlayerForfeitingCmd{},
	"NextTurnStartingCmd": &NextTurnStartingCmd{},
	"EndingGameCmd":       &EndingGameCmd{},
}

type EndingGameCmd struct {
	Command
	GameID       string `json:"gameId"`
	WinnerID     string `json:"winnerId"`
	LoserID      string `json:"loserId"`
	WinCondition string `json:"moveData"`
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


// ................ events ......................//
var eventTypeMap = map[string]interface{}{
	"GameAnnouncementEvent":        &GameAnnouncementEvent{},
	"DeclaredMoveEvent":            &DeclaredMoveEvent{},
	"InvalidMoveEvent":             &InvalidMoveEvent{},
	"OfficialMoveEvent":            &OfficialMoveEvent{},
	"GameStateUpdate":              &GameStateUpdate{},
	"GameStartEvent":               &GameStartEvent{},
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

//................. HANDLERS .....................//

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
		newMoveData := cmd.DeclaredMove
		moveParts := strings.Split(newMoveData, ".")
		
		//moveParts[2] is the moveCounter
		if moveParts[2] == "1" {
			// the first move is ALWAYS valid, so we can skip all of the below
			newEvent := &OfficialMoveEvent{
				GameID:    cmd.GameID,
				PlayerID:  cmd.SourcePlayerID,
				MoveData:  cmd.DeclaredMove,
				Timestamp: time.Now(),
			}
			d.timer.StopTimer() 
			d.EventDispatcher(newEvent)
			return

		} else if moveParts[2] == "2" {
			// the second move CAN be identical to the first move actually... but I still need to implement the SWAP mechanic so lets ignore this for now
		}

		lockedGame, ok := allLockedGames[cmd.GameID]
		if !ok {
			// handle this error ?
		}
		
		// pull moveList from redis cache, falling back onto successive persistence layers
		cacheKey := fmt.Sprintf("cache:%d:movelist", cmd.GameID)
		moveList := make(map[int]string)
		
		cacheErr := d.persister.cache.Once(&cache.Item{
			Key:   cacheKey,
			Value: &moveList, // The destination where moves will be loaded into
			Do: func(*cache.Item) (interface{}, error) {
				// If not in cache, fetch it from Redis
				ctxTime, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel() // Always defer a cancel for cleanup

				fetchedMoves, ok := d.fetchMoveList(ctxTime, cmd.GameID, &lockedGame)
				if !ok || errors.Is(ctxTime.Err(), context.DeadlineExceeded) {
					// fetchFromPostgres
				}
				return fetchedMoves, nil  // return the map you fetched, which will populate moveList
			},
		})

		// handle the cache error
		if cacheErr !=nil {
			panic(cacheErr)
		}
		
		// now, from our command, we can look into the moveList and see if the move is valid
		new_key, err := strconv.Atoi(moveParts[0]) // new_key is the move counter. 
		if err != nil {
			d.errorLogger.ErrorLog(ctx, "Error converting moveCounter to int", zap.Error(err))
		}
		new_val := moveParts[2] // new_val is the concatenated x and y coordinates of the most recent move
		
		maxKey := -1 // Start with an invalid value to ensure real keys replace this.

		// Checking if the value exists in the map and finding the max key.
		for key, value := range moveList {
			// Check for the maximum key
			if key > maxKey {
				maxKey = key
			}

			if value == new_val {
				// Value already exists in the moveList.
				d.errorLogger.ErrorLog(ctx, "Invalid, duplicate move detected", zap.String("Move", new_val), zap.String("GameID", cmd.GameID), zap.String("PlayerID", cmd.SourcePlayerID))
				
				newEvent := &InvalidMoveEvent{
					GameID:      cmd.GameID,
					PlayerID:    cmd.SourcePlayerID,
					InvalidMove: cmd.DeclaredMove,
				}
				d.EventDispatcher(newEvent)
				
				return
			}
		}

		if maxKey == -1 {
			// Log or handle the situation where no key was found.
			d.errorLogger.ErrorLog(ctx, "No key found in move list")
		}
		if maxKey+1 != new_key {
			// that means we missed a value. so we should go straight to postgres and try to fetch again. if it happens again, we panic
		}

		newEvent := &OfficialMoveEvent{
			GameID:    cmd.GameID,
			PlayerID:  cmd.SourcePlayerID,
			MoveData:  cmd.DeclaredMove,
			Timestamp: time.Now(),
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
		var newCmd *EndingGameCmd
		
		winCond := []string{"Forfeit: ",cmd.Reason}
		winCondition := strings.Join(winCond,"")

		if cmd.SourcePlayerID == lg.Player1.PlayerID {
			newCmd = &EndingGameCmd{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: winCondition,
			}
		} else if cmd.SourcePlayerID == lg.Player2.PlayerID {
			newCmd = &EndingGameCmd{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: winCondition,
			}
		} else {
			d.errorLogger.ErrorLog(ctx, "Error parsing the winner from AllLockedGames global", zap.String("LoserID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))
		}
		d.EventDispatcher(newCmd)

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

func (d *Dispatcher) fetchMoveList(ctx context.Context, GameID string, lockedGame *LockedGame) (map[int]string, bool) {

	pipe := d.persister.redis.Client.Pipeline()

	// Prepare commands for moves1 and moves2
	getMoves1 := pipe.HGetAll(ctx, fmt.Sprintf("MoveList:%s:%s", GameID, lockedGame.Player1.PlayerID))
	getMoves2 := pipe.HGetAll(ctx, fmt.Sprintf("MoveList:%s:%s", GameID, lockedGame.Player2.PlayerID))
	
	// Execute pipelined commands
	_, err := pipe.Exec(ctx)
	if err != nil {
		d.errorLogger.InfoLog(ctx, "error fetching moves from redis", zap.String("redis error", "executing pipeline"), zap.Error(err))

		return nil, false
	}
	
	// Fetch results
	moves1, err1 := getMoves1.Result()
	moves2, err2 := getMoves2.Result()
	if err1 != nil || err2 != nil {
		d.errorLogger.InfoLog(ctx, "error fetching moves from redis", zap.String("redis error", "fetching results after pipeline"), zap.Error(err1), zap.Error(err2))
		return nil, false
	}

	allMoves := make(map[int]string)

	for key, value := range moves1 {
		keyInt, _ := strconv.Atoi(key)
		allMoves[keyInt] = value
	}

	for key, value := range moves2 {
		keyInt, _ := strconv.Atoi(key)
		allMoves[keyInt] = value
	}

	// this below effectively replaced the two skipped errors within the above loops
	if len(allMoves)== 0 || len(allMoves) != len(moves1)+len(moves2) {
		d.errorLogger.InfoLog(ctx, "error fetching moves from redis: %v", zap.String("redis error", "combining maps"))
		return nil, false
	}

	return allMoves, true
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