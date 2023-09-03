package pregame

import (
	"atomic"
	"fmt"
	"sync"
	"time"
	
	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
	zap "go.uber.org/zap"
	_"github.com/mikeyg42/HEX/models"
)
 
// Define a shared map to keep track of locked games
var (
	allLockedGames = make(map[string]LockedGame)
	lockMutex      = sync.Mutex{}

	acknowledgments = make(map[string][]string)
	ackMutex        = sync.Mutex{}
)

func GenerateUniqueGameID(db *PostgresGameState, w *Worker) func() *RResult {
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

func (w *Worker) StartGame(player1, player2 PlayerIdentity) {
    game := NewGame(player1, player2)
    go w.runGame(game)
}

func (w *Worker) runGame(game Game) {
    // Handle game logic
    // Wait for the game to finish or receive external signals if needed
    // Once game is finished, inform the Lobby so the worker can be reused
}



