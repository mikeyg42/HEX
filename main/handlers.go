package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	cache "github.com/go-redis/cache/v9"
	hex "github.com/mikeyg42/HEX/models"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	timer "github.com/mikeyg42/HEX/timerpkg"
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
func (d *Dispatcher) handleEvent(done <-chan struct{}, event hex.Event, eventPubSub *redis.PubSub) error {
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
		timer.StopTimer()

		// This will parse the new move and then publish another message to the event bus that will be picked up by persisting logic and the win condition logic
		newEvt := Handler_OfficialMoveEvt(ctx, event, con)
		d.EventDispatcher(newEvt)

	case *hex.GameStateUpdate:
		// Persist the data and check win conditions
		newCmd := HandleGameStateUpdate(event)

		d.CommandDispatcher(newCmd)

		con.Timer.StopTimer() //always do this before starting timer again to ensure that the timer is flushed. It doesnt hurt to stop repeatedly.

	default:
		con.ErrorLog.Error(ctx, "unknown event type: %v", fmt.Errorf("weird event type: %v", event))
		con.EventCmdLog.Info(ctx, "Received Unusual, unknown event: %v", event)
	}
	return nil
}

func (d *Dispatcher) handleCommand(done <-chan struct{}, cmd hex.Command, commandPubSub *redis.PubSub) {
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
				GameID:    cmd.GameID,
				PlayerID:  cmd.SourcePlayerID,
				MoveData:  cmd.DeclaredMove,
				Timestamp: time.Now(),
			}
			con.Timer.StopTimer()
			d.EventDispatcher(newEvent)
			return

		} else if moveParts[2] == "2" {
			// the second move CAN be identical to the first move actually... but I still need to implement the SWAP mechanic so lets ignore this for now
		}

		lockedGame, ok := AllLockedGames[cmd.GameID]
		if !ok {
			// handle this error ?
		}

		// pull moveList from redis cache, falling back onto successive persistence layers
		cacheKey := fmt.Sprintf("cache:%d:movelist", cmd.GameID)
		moveList := make(map[int]string)

		cacheErr := con.Persister.Cache.Once(&cache.Item{
			Ctx:   ctx,
			Key:   cacheKey,
			Value: &moveList, // The destination where moves will be loaded into
			TTL:   time.Minute * 30,
			Do: func(*cache.Item) (interface{}, error) {
				// Fetches from 3-tiered persistence
				return con.retrieveMoveList(ctx, cmd.GameID, &lockedGame)
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
			GameID:    cmd.GameID,
			PlayerID:  cmd.SourcePlayerID,
			MoveData:  cmd.DeclaredMove,
			Timestamp: time.Now(),
		}

		d.EventDispatcher(newEvent)

	case *hex.NextTurnStartingCmd:

		con.EventCmdLog.Info(ctx, fmt.Sprintf("NextTurnStartingCmd received for game : %s", cmd.GameID))

		// Only Place you start the timer!
		con.Timer.StartTimer()

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

	case *hex.PlayerForfeitingCmd:

		con.EventCmdLog.Info(ctx, "Player forfeited the game", zap.String("PlayerID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))

		con.Timer.StopTimer()

		// Determine who the winner is via the allLockedGames struct
		lg := allLockedGames[cmd.GameID]
		var newCmd *hex.EndingGameCmd

		winCond := []string{"Forfeit: ", cmd.Reason}
		winCondition := strings.Join(winCond, "")

		if cmd.SourcePlayerID == lg.Player1.PlayerID {
			newCmd = &hex.EndingGameCmd{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player2.PlayerID,
				WinCondition: winCondition,
			}
		} else if cmd.SourcePlayerID == lg.Player2.PlayerID {
			newCmd = &hex.EndingGameCmd{
				GameID:       cmd.GameID,
				LoserID:      cmd.SourcePlayerID,
				WinnerID:     lg.Player1.PlayerID,
				WinCondition: winCondition,
			}
		} else {
			con.ErrorLog.ErrorLog(ctx, "Error parsing the winner from AllLockedGames global", zap.String("LoserID", cmd.SourcePlayerID), zap.String("GameID", cmd.GameID), zap.String("ForfeitReason", cmd.Reason))
		}
		d.EventDispatcher(newCmd)

	case *hex.LetsStartTheGameCmd: // this command is generated by the LockTheGame function found in the workerpool code

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
		con.Timer = MakeNewTimer()
		con.Timer.startChan <- struct{}{}

	}
}

func fetchMoveList(ctx context.Context, GameID string, lockedGame *hex.LockedGame, con *hex.GameContainer) (map[int]string, bool) {

	pipe := con.Persister.Redis.Client.Pipeline()

	// Prepare commands for moves1 and moves2
	getMoves1 := pipe.HGetAll(ctx, fmt.Sprintf("MoveList:%s:%s", GameID, lockedGame.Player1.PlayerID))
	getMoves2 := pipe.HGetAll(ctx, fmt.Sprintf("MoveList:%s:%s", GameID, lockedGame.Player2.PlayerID))

	// Execute pipelined commands
	_, err := pipe.Exec(ctx)
	if err != nil {
		con.ErrorLog.InfoLog(ctx, "error fetching moves from redis", zap.String("redis error", "executing pipeline"), zap.Error(err))

		return nil, false
	}

	// Fetch results
	moves1, err1 := getMoves1.Result()
	moves2, err2 := getMoves2.Result()
	if err1 != nil || err2 != nil {
		con.ErrorLog.InfoLog(ctx, "error fetching moves from redis", zap.String("redis error", "fetching results after pipeline"), zap.Error(err1), zap.Error(err2))
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
	if len(allMoves) == 0 || len(allMoves) != len(moves1)+len(moves2) {
		con.ErrorLog.InfoLog(ctx, "error fetching moves from redis: %v", zap.String("redis error", "combining maps"))
		return nil, false
	}

	return allMoves, true
}

func parseOfficialMoveEvt(ctx context.Context, e *hex.OfficialMoveEvent, con *hex.GameContainer) (*hex.GameStateUpdate, error) {
	// parses the OfficialMoveEvent and returns a GameStateUpdate struct, the next event in the progresssion, this struct is saveable in DB
	logger := con.ErrorLog
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

	update := &hex.GameStateUpdate{
		GameID:           gameID,
		PlayerID:         playerID,
		MoveCounter:      moveCounter,
		XCoordinate:      xCoordinate,
		YCoordinate:      yCoordinate,
		ConcatenatedMove: coordinates,
	}
	return update, nil
}

func Handler_OfficialMoveEvt(ctx context.Context, evt *hex.OfficialMoveEvent, con *hex.GameContainer) *hex.GameStateUpdate {

	newEvt, err := parseOfficialMoveEvt(ctx, evt, con)
	if err != nil {
		con.ErrorLog.ErrorLog(ctx, "error extracting move data", zap.Error(err))
	}
	return newEvt
}
