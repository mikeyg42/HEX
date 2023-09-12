package pregame

import (
	"context"
	"fmt"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	hex "github.com/mikeyg42/HEX/models"
	retry "github.com/mikeyg42/HEX/retry"
	logger "github.com/mikeyg42/HEX/storage"
	timer "github.com/mikeyg42/HEX/timerpkg"
	zap "go.uber.org/zap"
	storage "github.com/mikeyg42/HEX/storage"
)

type PlayerIdentity struct {
	PlayerID          string
	CurrentGameID     string
	CurrentOpponentID string
	CurrentPlayerRank int
	Username          string // Player's username (this is their actual username, whereas PlayerID will be a UUID+player1 or UUID+player2)
}

type Worker struct {
	WorkerID string
	GameChan chan hex.GameAnnouncementEvent 
	ResultsChan  chan hex.GameEndEvent
}

type Game struct {
	ID   int
	Data interface{}
	Id_PlayerA PlayerIdentity
	Id_PlayerB PlayerIdentity
}

type Lobby struct {
	GameChan       chan hex.GameAnnouncementEvent   // this chan will be populated by the matchmaking logic
	ResultsChan    chan hex.GameEndEvent // this chan will be populated by the workers relaying the results of the games
}




type GameController struct {
	WorkerID    string
	MsgChan   chan interface{}
	Persister *hex.GameStatePersister
	Errlog    *logger.Logger
	GameID    string
	Timer     *timer.TimerControl
	Id_PlayerA   string
	Id_PlayerB   string
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

func NewWorker(id string) *Worker {
	// Initialize the Worker
}

func (w *Worker) NewGameController(playerA, playerB string, con *hex.Container) *GameController {
	var gameID string

	// use retry package to generate a unique gameID that doesn't already exist in the database
	retryResult := w.GenerateUniqueGameID(con.Persister.Postgres)()
	if retryResult.Err == nil {
		gameID = retryResult.Message
	} else {
		panic(retryResult.Err)
	}
	
	return &GameController{
		WorkerID:    w.WorkerID,
		Persister: con.Persister,
		Errlog:    con.ErrorLog.(*logger.Logger),
		GameID:    gameID,
		Timer:     timer.MakeNewTimer(),
		Id_PlayerA:   playerA,
		Id_PlayerB:   playerB,
	}
}


func (w *Worker) runGame(game Game, con *hex.Container) {

	gc := w.NewGameController(game.Id_PlayerA.PlayerID, game.Id_PlayerB.PlayerID, con)

	ctx := context.Background()
	eventPubSub := gc.Persister.Redis.Client.Subscribe(ctx, "endEvent")
	defer eventPubSub.Close()

	// Publish the game announcement event
	msg := &hex.LetsStartTheGameCmd{
	GameID: gc.GameID,
	NextMoveNumber: 1,
	InitialPlayerID: gc.Id_PlayerA,
	SwapPlayerID: gc.Id_PlayerB,
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		// retry?? or just log and kms?
	}

	gc.Persister.Redis.Client.Publish(ctx, "Command", payload).Err()
	
	// after the game is started, we just need to wait for the game to end
	msgChan := eventPubSub.Channel()
	
	for {
		select {
		case msg := <-msgChan:
			// First, do a quick check if the message contains the expected event type
			if strings.Contains(msg.Payload, `"eventType:GameEndEvent"`) && strings.Contains(msg.Payload, fmt.Sprintf(`"gameID":"%s"`, gc.GameID)) {
				// fully unmarshal and process the message
				var evtMessage hex.GameEndEvent
				err := json.Unmarshal([]byte(msg.Payload),evtMessage)
				if err != nil {
					// Handle error
					continue
				}
	
				// Check if this message is for this worker and is of type "GameEndEvent"
				if evtMessage.GameID == gc.GameID {
					continue
				}

				// Tells the lobby that the game has ended
				w.ResultsChan <- evtMessage

				// ensure that the game has been all persisted to Postgres somehow?

				// clear all this games data from the Redis Cache

				// Remove the game from the locked games map
				hex.LockMutex.Lock()
				delete(hex.AllLockedGames, gc.GameID)
				hex.LockMutex.Unlock()
			}
			
		case <-ctx.Done():
			gc.Errlog.ErrorLog(ctx, "Context cancelled", zap.Bool("game complete?", false), zap.Error(ctx.Err()), zap.String("gameID", gc.GameID))
			
			moveList, err := storage.FetchMoveListFromCache(ctx, gc.GameID, con)
			if err !=nil {
				gc.Errlog.ErrorLog(ctx, "Game interrupted, but unable to fetch move list from cache", zap.Bool("game complete?", false), zap.String("gameID", gc.GameID))
				
			}
			// save the move list to the logger as a last ditch effort to persist game before closing
			gc.Errlog.InfoLog(ctx, "Game interrupted, saved move list from cache", zap.Bool("game complete?", true), zap.String("gameID", gc.GameID), zap.ObjectValues("moveList", moveList))

			// remove the game fro the locked games map
			hex.LockMutex.Lock()
			delete(hex.AllLockedGames, gc.GameID)
			hex.LockMutex.Unlock()

			return
		}
	}

}
