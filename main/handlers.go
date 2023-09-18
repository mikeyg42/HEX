package main

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	cache "github.com/go-redis/cache/v9"
	hex "github.com/mikeyg42/HEX/models"
	pubsub "github.com/mikeyg42/HEX/pubsub"
	storage "github.com/mikeyg42/HEX/storage"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
)

//................. HANDLERS .....................//

// Event Handler Wrapper
func (d *Dispatcher) handleEventWrapper(event interface{}) {
	d.EventChan <- event
}

// Command Handler Wrapper
func (d *Dispatcher) handleCommandWrapper(cmd interface{}) {
	d.CommandChan <- cmd
}

// Event Handler
func (d *Dispatcher) handleEvent(done <-chan struct{}, event pubsub.Eventer, eventPubSub *redis.PubSub) error {
	con := d.Container

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch event := event.(type) {
	case *hex.InvalidMoveEvent:
		con.ErrorLog.InfoLog(ctx, "Received InvalidMoveEvent: %v", event)

	case *hex.OfficialMoveEvent:
		strs := []string{event.GameID, event.PlayerID, "has made a move:", event.MoveData}
		con.EventCmdLog.InfoLog(ctx, strings.Join(strs, "/"))

		// To get here means the player's move was valid! Therefore, we can stop their countdown. we will start new one after processing this move
		d.Timer.StopTimer()

		// This will parse the new move and then publish another message to the event bus that will be picked up by persisting logic and the win condition logic
		newEvt := Handler_OfficialMoveEvt(ctx, event, con)
		d.EventDispatcher(newEvt)

	case *hex.GameStateUpdate:
		// Persist the data and check win conditions
		newCmd := HandleGameStateUpdate(event)

		d.CommandDispatcher(newCmd)

		d.Timer.StopTimer() //always do this before starting timer again to ensure that the timer is flushed. It doesnt hurt to stop repeatedly.

	default:
		con.EventCmdLog.InfoLog(ctx, "Received Unusual, unknown event: %v", zap.String("eventType", reflect.TypeOf(event).String()))
		con.ErrorLog.ErrorLog(ctx, "unknown event type: %v", zap.Error(fmt.Errorf("weird event type: %v", event)))

	}
	return nil
}

func (d *Dispatcher) handleCommand(done <-chan struct{}, cmd pubsub.Commander, commandPubSub *redis.PubSub) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	con := d.Container

	switch cmd := cmd.(type) {
	case *hex.DeclaringMoveCmd:
		newMoveData := cmd.DeclaredMove
		moveParts := strings.Split(newMoveData, ".")

		//moveParts[2] is the moveCounter
		if moveParts[2] == "1" {
			// the first move is ALWAYS valid, so we can skip all of the below
			newEvent := &hex.OfficialMoveEvent{
				Event:    pubsub.NewEvent(),
				GameID:   cmd.GameID,
				PlayerID: cmd.SourcePlayerID,
				MoveData: cmd.DeclaredMove,
			}
			d.Timer.StopTimer()
			d.EventDispatcher(newEvent)
			return

		} else if moveParts[2] == "2" {
			// the second move CAN be identical to the first move actually... but I still need to implement the SWAP mechanic so lets ignore this for now
		}

		// get the game from the global map
		lockedGame, ok := hex.AllLockedGames[cmd.GameID]
		if !ok {
			// handle this error ?
		}

		// pull moveList from redis cache, falling back onto successive persistence layers
		cacheKey := fmt.Sprintf("cache:%d:movelist", cmd.GameID)
		moveList := make(map[int]string)

		cacheErr := con.Persister.Redis.MyCache.Once(&cache.Item{
			Ctx:   ctx,
			Key:   cacheKey,
			Value: &moveList, // The destination where moves will be loaded into
			TTL:   time.Minute * 30,
			Do: func(*cache.Item) (interface{}, error) {
				// Fetches from 3-tiered persistence
				return storage.MasterFetchingMoveList(ctx, cmd.GameID, con.Persister.Redis, con.Persister.Postgres)
			},
		})

		// handle the cache error
		if cacheErr != nil {
			panic(cacheErr)
		}

		// now, from our command, we can look into the moveList and see if the move is valid
		new_key, err := strconv.Atoi(moveParts[0]) // new_key is the move counter.
		if err != nil {
			con.ErrorLog.ErrorLog(ctx, "Error converting moveCounter to int", zap.Error(err))
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
				con.ErrorLog.ErrorLog(ctx, "Invalid, duplicate move detected", zap.String("Move", new_val), zap.String("GameID", cmd.GameID), zap.String("PlayerID", cmd.SourcePlayerID))

				newEvent := &hex.InvalidMoveEvent{
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
			con.ErrorLog.ErrorLog(ctx, "No key found in move list")
		}
		if maxKey+1 != new_key {
			// that means we missed a value. so we should go straight to postgres and try to fetch again. if it happens again, we panic
		}

		newEvent := &hex.OfficialMoveEvent{
			GameID:   cmd.GameID,
			PlayerID: cmd.SourcePlayerID,
			MoveData: cmd.DeclaredMove,
		}

		d.EventDispatcher(newEvent)

	case *hex.NextTurnStartingCmd:

		con.EventCmdLog.InfoLog(ctx, fmt.Sprintf("NextTurnStartingCmd received for game : %s", cmd.GameID0))

		// Only Place you start the timer!
		d.Timer.StartTimer()
		// get pretty much the exact same tim e as the startTimer functionstarted
		startTime := time.Now()

		newEvt := &hex.TimerON_StartTurnAnnounceEvt{
			GameID:          cmd.GameID,
			ActivePlayerID:  cmd.NextPlayerID,
			ThisTurnEndTime: startTime.Add(moveTimeout),
			LastTurnEndTime: cmd.Timestamp(),
			MoveNumber:      cmd.UpcomingMoveNumber,
		}
		d.EventDispatcher(newEvt)

	case *hex.PlayerForfeitingCmd:

		con.EventCmdLog.InfoLog(ctx, "Player forfeited the game", zap.String("PlayerID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))

		d.Timer.ForfeitChan<- struct{}{} // this will stop the timer

		// Determine who the winner is via the hex.AllLockedGames struct
		lg := hex.AllLockedGames[cmd.GameID]
		var newEvt *hex.GameEndEvent

		winCond := []string{"Forfeit: ", cmd.Reason}
		winCondition := strings.Join(winCond, "")

		if cmd.SourcePlayerID == lg.Player1.PlayerID {
			newEvt = &hex.GameEndEvent{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: winCondition,
			}
		} else if cmd.SourcePlayerID == lg.Player2.PlayerID {
			newEvt = &hex.GameEndEvent{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: winCondition,
			}
		} else {
			con.ErrorLog.ErrorLog(ctx, "Error parsing the winner from AllLockedGames global", zap.String("LoserID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))
		}
		d.EventDispatcher(newEvt)

	case *hex.LetsStartTheGameCmd: // this command is generated by the LockTheGame function found in the workerpool code

		// Check if the game is not claimed by another goroutine!!!! Use LockMutex!!
		if isGameLocked(cmd.GameID) {

			// .......................................//
			// ........... THIS IS missing! ............//
			// .......................................//

			return
		}
	// MAKE THE TIMER AND START IT INITIALLY
	d.Timer.StartTimer()

		// initiate the game start with the locked players in a new goroutine
		startEvent := &hex.GameStartEvent{
			GameID:     cmd.GameID,
			PlayerA_ID: cmd.InitialPlayerID,
			PlayerB_ID: cmd.SwapPlayerID,
			TimerEvt: &hex.TimerON_StartTurnAnnounceEvt{
				GameID:         cmd.GameID,
				ActivePlayerID: cmd.InitialPlayerID,
				LastTurnEndTime: time.Now(),
				ThisTurnEndTime:    time.Now().Add(moveTimeout),
				MoveNumber:     1,
			},
		}

		d.EventDispatcher(startEvent)

		

	case *hex.InitialTilePlayed:
		d.Timer.StopTimer()

	//.....

	case *hex.SecondTurnPlayed:
		//.....

	}

}

func isGameLocked(key string) bool {
	hex.LockMutex.Lock()         // Locking the mutex to ensure exclusive access
	defer hex.LockMutex.Unlock() // Unlocking it in defer to ensure it's released regardless of how we exit the function

	_, exists := hex.AllLockedGames[key]
	return exists
}

func parseOfficialMoveEvt(ctx context.Context, e *hex.OfficialMoveEvent, con *hex.Container) (*hex.GameStateUpdate, error) {
	// parses the OfficialMoveEvent and returns a GameStateUpdate struct, the next event in the progresssion, this struct is saveable in DB
	logger := con.ErrorLog
	moveParts := strings.Split(e.MoveData, ".")

	// moveParts is the split string of an integer 5.B.2A ://meaning on the 5th move, player B played on coordinates 2A
	if len(moveParts) != 3 || len(moveParts) != 2 {
		// log the error and then exit - this should be a development only error
		logger.ErrorLog(ctx, "moveData format error", zap.String("gameID", e.GameID), zap.String("moveData", e.MoveData))
		return nil, fmt.Errorf("invalid moveData format")
	}

	// convert the moveCounter to an int
	moveCounter, err := strconv.Atoi(moveParts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid moveCounter: %w", moveParts[0], zap.Error(err))
	}

	gameID := e.GameID
	playerID := moveParts[1] // "kinda their ID"

	coordinates := moveParts[2]
	if len(coordinates) < 2 {
		return nil, fmt.Errorf("missing at least part of the coordinates")
	}

	xCoordinate := coordinates[:1]
	yCoordinate, err := strconv.Atoi(coordinates[1:])
	if err != nil {
		return nil, fmt.Errorf("invalid yCoordinate: %w", err)
	}

	update := &hex.GameStateUpdate{
		GameID:           gameID,
		PlayerID:         playerID,
		MoveCounter:      moveCounter, // this refers to the counter in the whole game (not a players 5th move, it would be 6th move of the whole game)
		XCoordinate:      xCoordinate, // this is a letter (ie a string)
		YCoordinate:      yCoordinate, // this is an int
		ConcatenatedMove: coordinates, // this is a list of every move played in the game (to confirm a player cannnot play on top of another tile... )
	}
	return update, nil
}

func Handler_OfficialMoveEvt(ctx context.Context, evt *hex.OfficialMoveEvent, con *hex.Container) *hex.GameStateUpdate {

	newEvt, err := parseOfficialMoveEvt(ctx, evt, con)
	if err != nil {
		con.ErrorLog.ErrorLog(ctx, "error extracting move data", zap.Error(err))
	}
	return newEvt
}
