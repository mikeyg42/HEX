package pregame

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
	hex "github.com/mikeyg42/HEX/models"
	retry "github.com/mikeyg42/HEX/retry"
	logger "github.com/mikeyg42/HEX/storage"
	storage "github.com/mikeyg42/HEX/storage"
	timer "github.com/mikeyg42/HEX/timerpkg"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	gormlogger "gorm.io/gorm/logger"
)

type PlayerIdentity struct {
	PlayerID          string
	CurrentGameID     string
	CurrentOpponentID string
	CurrentPlayerRank int
	Username          string // Player's username (this is their actual username, whereas PlayerID will be a UUID+player1 or UUID+player2)
}

type Worker struct {
	WorkerID    string
	GamesChan   chan hex.MatchmakingEvt
	ResultsChan chan *hex.GameEndEvent
}

type Game struct {
	ID         int
	Data       interface{}
	Id_PlayerA PlayerIdentity
	Id_PlayerB PlayerIdentity
}

type Lobby struct {
	GamesChan   chan hex.MatchmakingEvt // this chan will be populated by the matchmaking logic
	ResultsChan chan *hex.GameEndEvent   // this chan will be populated by the workers relaying the results of the games
}



func (w *Worker) GenerateUniqueGameID(db *hex.PostgresGameState) func() *retry.RResult {
	return func() *retry.RResult {
		var gameID string
		var count int64

		retryFunc := func() (string, error) {
			gameID = fmt.Sprintf("%s-%s", uuid.New().String(), w.WorkerID)
			if errSearch := db.DB.Model(&hex.GameStateUpdate{}).Where("game_id = ?", gameID).Count(&count).Error; errSearch != nil {
				return "", fmt.Errorf("failed to check for existing gameID: %w", errSearch)
			}
			if count > 0 {
				// This means the gameID already exists, returning an error to trigger retry.
				return "", fmt.Errorf("gameID already exists")
			}
			return gameID, nil
		}

		r := retry.RetryFunc(context.TODO(), retryFunc)
		return r
	}
}

func NewWorker(id string, gChan chan hex.GamesChan) *Worker {
	// Initialize the Worker


	return  &Worker{
		WorkerID:    id,
		GamesChan:   gChan,
		ResultsChan: rChan,
	}
}

// GameController is a struct that contains everything the worker needs to run a game. it is destroyed at the end a game and recreated later for endxt game
func (w *Worker) NewGameController(playerA, playerB string, con *hex.Container) *GameController {
	var gameID string

	// use retry package to generate a unique gameID that doesn't already exist in the database
	retryResult := w.GenerateUniqueGameID(con.Persister.Postgres)()
	if retryResult.Err == nil {
		gameID = retryResult.Message
	} else {
		panic(retryResult.Err)
	}

	timerLogger := storage.InitLogger(path.Join(con.Config.LogPath, "timer.log"), gormlogger.Info)
	timerCtx := context.WithValue(context.Background(), hex.TimerLoggerKey, timerLogger)

	return &GameController{
		WorkerID:   w.WorkerID,
		Persister:  con.Persister,
		Errlog:     con.ErrorLog.(*logger.Logger),
		GameID:     gameID,
		Timer:      timer.MakeNewTimer(timerCtx),
		Id_PlayerA: playerA,
		Id_PlayerB: playerB,
	}
}

func (w *Worker) runGame(game Game, con *hex.Container) {

	gc := w.NewGameController(game.Id_PlayerA.PlayerID, game.Id_PlayerB.PlayerID, con)

	gameCtx, cancel := context.WithCancel(context.Background())
	eventPubSub := gc.Persister.Redis.Client.Subscribe(gameCtx, "endEvent")
	defer eventPubSub.Close()
	defer cancel()

	w.initiateNewGame(gameCtx, eventPubSub, gc)
	w.waitForGameEnding(gameCtx, eventPubSub, gc) // this will block until the game is over

}

func (w *Worker) CloseGame(ctx context.Context, evtMessage *hex.GameEndEvent, gc *GameController) {

	// Tells the lobby that the game has ended
	w.ResultsChan <- evtMessage

	// ensure that the game has been all persisted to Postgres somehow?

	// clear all this games data from the Redis Cache
	cacheKey := fmt.Sprintf("cache:%s:movelist", gc.GameID)
	err := gc.Persister.Redis.MyCache.Delete(ctx, cacheKey)

	if err != nil {
		gc.Errlog.ErrorLog(ctx, "Error deleting game from Redis cache", zap.Bool("game complete?", true), zap.Error(err), zap.String("gameID", gc.GameID))
	}

	// Remove the game from the locked games map
	gc.DeleteGameFromLockedGamesMap(ctx, gc.GameID)

}

func (w *Worker) initiateNewGame(ctx context.Context, gc *GameController) {
	// Create and then publish the game announcement event
	startMsg := &hex.LetsStartTheGameCmd{
		GameID:          gc.GameID,
		NextMoveNumber:  1,
		InitialPlayerID: gc.Id_PlayerA,
		SwapPlayerID:    gc.Id_PlayerB,
	}

	// marshall the message and publish it to the command pubsub
	startMsg_payload, err := json.Marshal(startMsg)
	if err != nil {
		gc.Errlog.ErrorLog(ctx, "Error marshalling startMsg_payload", zap.Bool("game complete?", false), zap.Error(err), zap.String("gameID", gc.GameID))
		panic(err) //?
	}

	// try to publish message and if it doesnt work retry a few times, then panic if it fails. rResult will have the error if it fails, as well as two fields that dont matter
	rResult := retry.RetryFunc(ctx, func() error {
		return gc.Persister.Redis.Client.Publish(ctx, "Command", startMsg_payload).Err()
	})
	err = rResult.Err
	if err != nil {
		gc.Errlog.ErrorLog(ctx, "Error publishing startMsg_payload, despite retrys", zap.Bool("game complete?", false), zap.Error(err), zap.String("gameID", gc.GameID))
	}

	return
}

func (w *Worker) waitForGameEnding(ctx context.Context, eventPubSub *redis.PubSub, gc *GameController) {

	// after the game is started, we just need to wait for the game to end
	msgChan := eventPubSub.Channel() //channel = endEvent
	errlog := gc.Errlog.ErrorLog

	for {
		select {
		case msg := <-msgChan: // msg will be unmarshalled
			// First, do a quick check if the message contains the expected event type
			if strings.Contains(msg.Payload, `"eventType:GameEndEvent"`) && strings.Contains(msg.Payload, fmt.Sprintf(`"gameID":"%s"`, gc.GameID)) {
				// fully unmarshal and process the message
				var evtMessage *hex.GameEndEvent

				r := retry.RetryFunc(ctx, func() *retry.RResult {
					err := json.Unmarshal([]byte(msg.Payload), evtMessage)
					return &retry.RResult{
						Err: err, 
						Message: "", 
						Interface: evtMessage,
					}
				})

				if r.Err != nil {
					err := fmt.Errorf("Error unmarshalling endEvent, despite retrys: %s", r.Err.Error)
					errlog(ctx, "Error unmarshalling endEvent, despite retrys", zap.Bool("game complete?", false), zap.Error(err), zap.String("gameID", gc.GameID))
					panic(err)
				}

				// Check if this message is for this worker and is of type "GameEndEvent"
				if evtMessage.GameID != gc.GameID {
					gc.Errlog.InfoLog(ctx, "checking string of unmarshalled message failed", zap.Bool("game complete?", false), zap.String("gameID", gc.GameID))
					continue
				}

				w.CloseGame(ctx, evtMessage, gc)

				return
			}

		case <-ctx.Done():
			errlog(ctx, "Context cancelled", zap.Bool("game complete?", false), zap.Error(ctx.Err()), zap.String("gameID", gc.GameID))

			moveList, err := storage.FetchMoveListFromCache(ctx, gc.GameID, gc.Persister.Redis)
			if err != nil {
				errlog(ctx, "Game interrupted, but unable to fetch move list from cache", zap.Bool("game complete?", false), zap.String("gameID", gc.GameID), zap.Error(err))
			}
			// save the move list to the logger as a last ditch effort to persist game before closing
			gc.Errlog.InfoLog(ctx, "Game interrupted, saved move list from cache", zap.Bool("game complete?", true), zap.String("gameID", gc.GameID), zap.ObjectValues("moveList", moveList))

			// remove the game fro the locked games map
			gc.DeleteGameFromLockedGamesMap(ctx, gc.GameID)

			return
		}
	}
}

func (gc *GameController) DeleteGameFromLockedGamesMap(ctx context.Context, gameID string) {
	
	hex.LockMutex.Lock()
	defer hex.LockMutex.Unlock()
	delete(hex.AllLockedGames, gameID)

	// test its indeed gone
	_, ok := hex.AllLockedGames[gameID]
	if ok {
		errMsg := fmt.Errorf("gameID %s still in locked games map", gameID)
		gc.Errlog.ErrorLog(ctx, "failure to delete key value from locked games map ", zap.Bool("game complete?", true), zap.Error(errMsg), zap.String("gameID", gc.gameID))
		panic(errMsg)
	} 

	return
}
