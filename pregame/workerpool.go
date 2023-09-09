package pregame

import (
	"context"
	"fmt"
	"sync"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	hex "github.com/mikeyg42/HEX/models"
	retry "github.com/mikeyg42/HEX/retry"
	logger "github.com/mikeyg42/HEX/storage"
	timer "github.com/mikeyg42/HEX/timerpkg"
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
	GameChan chan Game //
	Results  chan Result
}

type Game struct {
	ID   int
	Data interface{}
	Id_PlayerA PlayerIdentity
	Id_PlayerB PlayerIdentity
}

type Result struct {
	GameID int
	Result interface{}
}

type Lobby struct {
	Games   chan Game   // this chan will be populated by the matchmaking logic
	Results chan Result // this chan will be populated by the workers relaying the results of the games
}

// Define a struct to represent a locked game with two players
type LockedGame struct {
	WorkerID      string
	GameID        string
	InitialPlayer PlayerIdentity // until after second turn there won't be a player 1 and 2, because of the swap mechanic
	SwapPlayer    PlayerIdentity
	Player1       PlayerIdentity
	Player2       PlayerIdentity
}

// Define a shared map to keep track of locked games
var (
	allLockedGames = make(map[string]LockedGame)
	lockMutex      = sync.Mutex{}

	acknowledgments = make(map[string][]string)
	ackMutex        = sync.Mutex{}
)

type GameController struct {
	Worker    string
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
	
	return 	w.InitializeGS(con, gameID, playerA, playerB)
}

func (w *Worker) InitializeGS(con *hex.Container, gameID string, playerA, playerB string) *GameController {
	return &GameController{
		Worker:    w.WorkerID,
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
	eventPubSub := gc.Persister.Redis.Client.Subscribe(ctx, "events")
	defer eventPubSub.Close()

	msgChan := eventPubSub.Channel()
	
	for {
		select {
		case msg := <-msgChan:
			// First, do a quick check if the message contains the expected event type
			if strings.Contains(msg.Payload, `"eventType:GameEndEvent"`) && strings.Contains(msg.Payload, fmt.Sprintf(`"gameID":"%s"`, gc.GameID)) {
				// fully unmarshal and process the message
				var eventMessage struct {
					GameID    string `json:"gameID"`
					EventType string `json:"eventType"`
					Data      map[string]hex.GameEndEvent `json:"data"`
				}
				err := json.Unmarshal([]byte(msg.Payload), &eventMessage)
				if err != nil {
					// Handle error
					continue
				}
	
				// Check if this message is for this worker and is of type "GameEndEvent"
				if eventMessage.GameID == gc.GameID && eventMessage.EventType == "GameEndEvent" {
					continue
				}

				results := eventMessage.Data[gc.GameID]

				moveLog :=results.CombinedMoveLog
				winner := results.WinnerID
				loser := results.LoserID
				winCondition := results.WinCondition


				// Handle the GameEndingEvent for this game instance
				Results <- Result{GameID: game.ID, Result: result}
			}
			
		case <-ctx.Done():
			return
		}
	}

}
