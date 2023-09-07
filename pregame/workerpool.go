package pregame

import (
	"atomic"
	"fmt"
	"sync"
	"time"
	
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	hex "github.com/mikeyg42/HEX/models"
	timer "github.com/mikeyg42/HEX/timerpkg"
	logger "github.com/mikeyg42/HEX/storage"
)
 
// Define a shared map to keep track of locked games
var (
	allLockedGames = make(map[string]LockedGame)
	lockMutex      = sync.Mutex{}

	acknowledgments = make(map[string][]string)
	ackMutex        = sync.Mutex{}
)

type GameState struct {
	Worker string
	Persister *hex.GameStatePersister
	Errlog  *logger.Logger
	GameID string
	Timer *timer.TimerControl
}

func (w *Worked)GenerateUniqueGameID(db *hex.PostgresGameState) func() *hex.RResult {
	return func() *RResult {
		var gameID string

		for {
			gameID = fmt.Sprintf("%s-%s", uuid.New().String(), w.WorkerID)

			var count int64
			if errSearch := db.DB.Model(&GameStateUpdate{}).Where("game_id = ?", gameID).Count(&count).Error; errSearch != nil {
				return &RResult{
					Err:     fmt.Errorf("failed to check for existing gameID: %w", errSearch),
					Message: "",
				}
			}

			if count == 0 {
				// Found an unused gameID, break the loop
				break
			}

			// Add a delay for DB overload
			time.Sleep(time.Millisecond * 100)
		}

		return &RResult{
			Err:     nil,
			Message: gameID,
		}
	}
}

func NewWorker(id string) *Worker {
    // Initialize the Worker
}


func (w *Worker) NewGame(playerA, playerB hex.PlayerIdentity, con *hex.GameContainer) hex.GameState {

gameID := w.GenerateUniqueGameID(con.Persister.Postgres)
gs := w.InitializeGS(con, gameID)
commandPubSub := gs.Persister.Redis.Client.Subscribe(ctx, "commands")
eventPubSub := gs.Persister.Redis.Client.Subscribe(ctx, "events")

go w.runGame(game) 
//async stuff

return gs
}


func (w *Worker) InitializeGS(con *hex.GameContainer, gameID string) *GameState {
	return &GameState{
		Worker: w.WorkerID,
		Persister: con.Persister,
		Errlog: con.ErrorLog,
		GameID: gameID,
		timer: ti.MakeNewTimer(),
	}
} 


func (w *Worker) runGame(game Game) {
    // Handle game logic
    // Wait for the game to finish or receive external signals if needed
    // Once game is finished, inform the Lobby so the worker can be reused

}



