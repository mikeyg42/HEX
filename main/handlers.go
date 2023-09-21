package main

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	logic "github.com/mikeyg42/HEX/logic"
	hex "github.com/mikeyg42/HEX/models"
	pubsub "github.com/mikeyg42/HEX/pubsub"
	retry "github.com/mikeyg42/HEX/retry"
	storage "github.com/mikeyg42/HEX/storage"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
)

//................. HANDLERS .....................//

// Event Handler Wrapper
func (d *Dispatcher) handleEventWrapper(event interface{}) {
	d.EventChan <- event
}

// Event Handler
func (d *Dispatcher) handleEvent(done <-chan struct{}, event pubsub.Eventer, eventPubSub *redis.PubSub) error {
	con := d.Container

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	switch event := event.(type) {
	case *hex.InvalidMoveEvent:
		d.Logger.InfoLog(ctx, "Received InvalidMoveEvent", zap.String("GameID", event.GameID), zap.String("PlayerID", event.PlayerID), zap.String("InvalidMove", event.InvalidMove))

	case *hex.OfficialMoveEvent:
		strs := []string{event.GameID, event.PlayerID, "has made a move:", event.MoveData}
		d.Logger.InfoLog(ctx, strings.Join(strs, "/"))

		// To get here means the player's move was valid! Therefore, we can stop their countdown. we will start new one after processing this move
		d.Timer.StopTimer()

		// This will parse the new move and then publish another message to the event bus that will be picked up by persisting logic and the win condition logic
		newEvt := d.Handler_OfficialMoveEvt(ctx, event)
		//newEvt is type  *hex.GameStateUpdate
		d.EventDispatcher(newEvt)

	case *hex.GameStateUpdate:
		// Persist the data and check win conditions
		newEvt := d.HandleGameStateUpdate(event)

		// ...?

		d.EventDispatcher(newEvt)

		d.Timer.StopTimer() //always do this before starting timer again to ensure that the timer is flushed. It doesnt hurt to stop repeatedly.
	case *hex.DeclaringMoveCmd:

		newMoveData := event.DeclaredMove
		moveParts := strings.Split(newMoveData, ".")

		//moveParts[2] is the moveCounter
		if moveParts[2] == "1" || moveParts[2] == "2" {
			// these moves are handled by independent logic and should not trigger this command
			d.Logger.InfoLog(ctx, "Invalid command delivered to command bus", zap.String("Move", newMoveData), zap.String("GameID", evt.GameID), zap.String("PlayerID", evt.SourcePlayerID))
		}

		// pull moveList from redis cache, falling back onto successive persistence layers
		moveList := make(map[int]string)
		moveList, err := storage.MasterFetchingMoveList(ctx, event.GameID, con.Persister.Redis, con.Persister.Postgres)
		// handle the retrieval error
		if err != nil {
			panic(err)
		}

		// now, from our command, we can look into the moveList and see if the move is valid
		new_key, err := strconv.Atoi(moveParts[0]) // new_key is the move counter.
		if err != nil {
			d.Logger.ErrorLog(ctx, "Error converting moveCounter to int", zap.Error(err))
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
				d.Logger.ErrorLog(ctx, "Invalid, duplicate move detected", zap.String("Move", new_val), zap.String("GameID", evt.GameID), zap.String("PlayerID", evt.SourcePlayerID))

				newEvent := &hex.InvalidMoveEvent{
					GameID:      event.GameID,
					PlayerID:    event.SourcePlayerID,
					InvalidMove: event.DeclaredMove,
				}
				d.EventDispatcher(newEvent)
				return nil
			}
		}

		if maxKey == -1 {
			// Log or handle the situation where no key was found.
			con.ErrorLog.ErrorLog(ctx, "No key found in move list")
		}
		if maxKey+1 != new_key {
			// that means we missed a value. so we should go straight to postgres and try to fetch again. if it happens again, we panic

			// this needs to be handled!!
		}

		newEvent := &hex.OfficialMoveEvent{
			GameID:   event.GameID,
			PlayerID: event.SourcePlayerID,
			MoveData: event.DeclaredMove,
		}

		d.EventDispatcher(newEvent)

	case *hex.NextTurnStartingCmd:
		// it doesn't hurt to call this all the time but it's not necessary
		d.Timer.StopTimer()

		//log the cmd
		con.EventCmdLog.InfoLog(ctx, fmt.Sprintf("NextTurnStartingCmd received for game : %s", event.GameID))

		// Only Place you start the timer!
		d.Timer.StartTimer()
		// get pretty much the exact same tim e as the startTimer functionstarted
		startTime := time.Now()
		newEvt := &hex.TimerON_StartTurnAnnounceEvt{
			GameID:          event.GameID,
			ActivePlayerID:  event.NextPlayerID,
			ThisTurnEndTime: startTime.Add(moveTimeout),
			LastTurnEndTime: event.Timestamp(), // this function call should give the timestamp of any evt or cmd
			MoveNumber:      event.UpcomingMoveNumber,
		}
		d.EventDispatcher(newEvt)

	case *hex.PlayerForfeitingCmd:

		con.EventCmdLog.InfoLog(ctx, "Player forfeited the game", zap.String("PlayerID", event.SourcePlayerID), zap.String("GameID", evt.GameID), zap.String("ForfeitReason", evt.Reason))

		d.Timer.ForfeitChan <- struct{}{} // this will stop the timer

		// Determine who the winner is via the hex.AllLockedGames struct
		lg := hex.AllLockedGames[event.GameID]
		var newEvt *hex.GameEndEvent

		winCond := []string{"Forfeit: ", event.Reason}
		winCondition := strings.Join(winCond, "")

		if event.SourcePlayerID == lg.Player1.PlayerID {
			newEvt = &hex.GameEndEvent{
				GameID:       event.GameID,
				LoserID:      event.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: winCondition,
			}
		} else if event.SourcePlayerID == lg.Player2.PlayerID {
			newEvt = &hex.GameEndEvent{
				GameID:       event.GameID,
				LoserID:      event.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: winCondition,
			}
		} else {
			con.ErrorLog.ErrorLog(ctx, "Error parsing the winner from AllLockedGames global", zap.String("LoserID", evt.SourcePlayerID), zap.String("GameID", evt.GameID), zap.String("ForfeitReason", evt.Reason))
		}
		d.EventDispatcher(newEvt)

	case *hex.LetsStartTheGameCmd: // this command is generated by the LockTheGame function found in the workerpool code

		// Check if the game is not claimed by another goroutine!!!!
		if isGameLocked(event.GameID) {
			// if it is, we need to kill the worker that is trying to start the duplicate game
			lg := accessLockedGame(event.GameID)
			workerID := lg.WorkerID
			killWorker(workerID)
			// DEFINE A KILL WORKER FUNCTION
			return nil
		}

		//  we should add here to have both players ack !?!

		// MAKE THE TIMER AND START IT INITIALLY
		d.Timer.StartTimer()

		// initiate the game start with the locked players in a new goroutine
		startEvent := &hex.GameStartEvent{
			GameID:     event.GameID,
			PlayerA_ID: event.InitialPlayerID,
			PlayerB_ID: event.SwapPlayerID,
			TimerEvt: &hex.TimerON_StartTurnAnnounceEvt{
				GameID:          event.GameID,
				ActivePlayerID:  event.InitialPlayerID,
				LastTurnEndTime: time.Now(), // not actually there was no last rurn
				ThisTurnEndTime: time.Now().Add(moveTimeout),
				MoveNumber:      1,
			},
		}

		d.EventDispatcher(startEvent)

	default:
		con.EventCmdLog.InfoLog(ctx, "Received Unusual, unknown event: %v", zap.String("eventType", reflect.TypeOf(event).String()))
		d.Logger.ErrorLog(ctx, "unknown event type", zap.Error(fmt.Errorf("weird event type: %v", event)))

	}
	return nil
}

func isGameLocked(key string) bool {
	hex.LockMutex.Lock()         // Locking the mutex to ensure exclusive access
	defer hex.LockMutex.Unlock() // Unlocking it in defer to ensure it's released regardless of how we exit the function

	_, exists := hex.AllLockedGames[key]

	return exists
}

func parseOfficialMoveEvt(ctx context.Context, e *hex.OfficialMoveEvent) (*hex.GameStateUpdate, error) {
	// parses the OfficialMoveEvent and returns a GameStateUpdate struct, the next event in the progresssion, this struct is saveable in DB
	moveParts := strings.Split(e.MoveData, ".")

	// moveParts is the split string of an integer 5.B.2A ://meaning on the 5th move, player B played on coordinates 2A
	if len(moveParts) != 3 || len(moveParts) != 2 {
		// log the error and then exit - this should be a development only error
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

func (d *Dispatcher) Handler_OfficialMoveEvt(ctx context.Context, evt *hex.OfficialMoveEvent) {

	updateToBePersisted, err := parseOfficialMoveEvt(ctx, evt)
	d.Logger.ErrorLog(ctx, "error parsing OfficialMoveEvent", zap.Error(err))

	d.PersistNewMove(ctx, updateToBePersisted, d.PG, d.RS)

	// get all the info needed to start the timer again, and then do so
	moveParts := strings.Split(evt.MoveData, ".")
	moveCounter, err := strconv.Atoi(moveParts[0])
	if err != nil {
		d.Logger.ErrorLog(ctx, "error extracting moveCounter", zap.Error(err))
	}

	lg := accessLockedGame(evt.GameID)

	var nextPlayer string
	if lg.InitialPlayer.PlayerID == evt.PlayerID {
		nextPlayer = lg.SwapPlayer.PlayerID
	} else {
		nextPlayer = lg.InitialPlayer.PlayerID
	}

	newEvt := &hex.TimerON_StartTurnAnnounceEvt{
		GameID:          evt.GameID,
		LastTurnEndTime: time.Now(),
		ThisTurnEndTime: time.Now().Add(moveTimeout),
		MoveNumber:      moveCounter + 1,
		ActivePlayerID:  nextPlayer,
	}
	d.Timer.StartTimer()
	d.EventDispatcher(newEvt)

	return

}

func (d *Dispatcher) PersistNewMove(ctx context.Context, newMove *hex.GameStateUpdate, pg *hex.PostgresGameState, rs *hex.RedisGameState) interface{} {

	logger := d.Logger.ErrorLog

	playerID := newMove.PlayerID
	moveCounter := newMove.MoveCounter
	gameID := newMove.GameID
	xCoord := newMove.XCoordinate
	yCoord := newMove.YCoordinate

	newVert, err := logic.ConvertToTypeVertex(xCoord, yCoord)
	if err != nil {
		//handle
	}

	// retrieve adjacency matrix and movelist from redis, with postgres as fall back option
	oldAdjG, oldMoveList := storage.FetchGameState(ctx, gameID, playerID, rs, pg)
	newAdjG, newMoveList := logic.IncorporateNewVert(ctx, oldMoveList, oldAdjG, newVert)

	// update postgres too
	err = storage.PersistGameState_sql(ctx, newMove, pg)
	if err != nil {
		logger(ctx, "failure to persist to postgresql", zap.Error(err), zap.String("gameID", gameID), zap.String("playerID", playerID), zap.Int("moveCounter", moveCounter))
	}

	var wg sync.WaitGroup
	wg.Add(3) // two persistence actions
	winChan := make(chan bool)

	go func() {
		defer wg.Done()
		win_yn := logic.EvalWinCondition(newAdjG, newMoveList)
		winChan <- win_yn
	}()
	go func() {
		defer wg.Done()
		r := retry.RetryFunc(ctx, func() (string, interface{}, error) {
			err := storage.PersistGraphToRedis(ctx, newAdjG, gameID, playerID, rs)
			return "", nil, err
		})
		if r.Err != nil {
			// Handle the error, possibly by logging it
			logger(ctx, "Failed to persist graph to Redis: %v", zap.Error(r.Err))
		}
	}()

	go func() {
		defer wg.Done()
		r := retry.RetryFunc(ctx, func() (string, interface{}, error) {
			err := storage.PersistMoveToRedisList(ctx, newVert, gameID, playerID, rs)
			return "", nil, err
		})
		if r.Err != nil {
			// Handle the error, possibly by logging it
			logger(ctx, "Failed to persist move List to Redis", zap.Error(r.Err))
		}
	}()

	wg.Wait()           // Wait for all actions to complete
	win_yn := <-winChan // extract the win condition from the channel
	close(winChan)

	if win_yn {
		// use the lockedGames map to ascertain who opponent is (ie who's turn is next )
		lg := accessLockedGame(gameID)

		loserID := lg.Player1.PlayerID
		if lg.Player1.PlayerID == playerID {
			loserID = lg.Player2.PlayerID
		}

		newEvt := &hex.GameEndEvent{
			GameID:   gameID,
			WinnerID: playerID,
			LoserID:  loserID,

			WinCondition: "A True Win",
		}
		return newEvt

	} else {
		// THIS NEEDS TO BE FIXED
		nextPlayer := "P2"
		if playerID != "P1" {
			nextPlayer = "P1"
		}

		newEvt := &hex.NextTurnStartingCmd{
			GameID:             gameID,
			PriorPlayerID:      playerID,
			NextPlayerID:       nextPlayer,
			UpcomingMoveNumber: moveCounter + 1,
		}
		return newEvt
	}

	// include a check to compare redis and postgres?
}

func accessLockedGame(gameID string) *hex.LockedGame {
	hex.LockMutex.Lock()
	defer hex.LockMutex.Unlock()

	lg := hex.AllLockedGames[gameID]

	return lg
}
