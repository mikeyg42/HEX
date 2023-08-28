package pregame

import (
	"fmt"
	"sync"
	"time"

	_ "github.com/mikeyg42/HEX/models"
)

const (
	maxWorkers        = 5
	maxQueueSize      = 100
	shutdownThreshold = 3
	initialNumWorkers = 3
)

func NewLobby() *Lobby {
	return &Lobby{
		games:   make(chan Game, 0),
		results: make(chan Result, 0),
	}
}
func (l *Lobby) Run() {
	var wg sync.WaitGroup

	// Create initial workers
	for w := 1; w <= initialNumWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			worker(workerID, l.games, l.results)
			wg.Done()
		}(w)
	}

	numWorkers := initialNumWorkers
	// Monitor the games channel length
	go func() {
		for range time.Tick(time.Second) {
			queueSize := len(l.games)
			if queueSize > maxQueueSize && numWorkers < maxWorkers {
				// Increase the number of workers
				numWorkers++

				fmt.Println("Increasing the number of workers to", numWorkers)
				wg.Add(1)

				go func(workerID int) {
					worker(workerID, l.games, l.results)
					wg.Done()
				}(numWorkers)

			} else if queueSize < maxQueueSize/shutdownThreshold && numWorkers > 1 {
				// Decrease the number of workers
				fmt.Println("Decreasing the number of workers to", numWorkers-1)
				numWorkers--
			}
		}
	}()

	// Wait for all workers to finish or handle shutdown signal
	waitForShutdown := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitForShutdown)
	}()

	// Collect results
	go func() {
		for r := range l.results {
			fmt.Println("Game", r.GameID, "result:", r.Result)
		}
	}()

	// Assign games to workers
	// This can be populated by your matchmaking logic
	/*     for j := 1; j <= numGames; j++ {
	       l.games <- Game{ID: j, Data: j}
	   } */

	// Graceful shutdown
	<-waitForShutdown
	close(l.results)
	fmt.Println("Worker pool shutdown")
}

func main() {
	lobby := NewLobby()
	lobby.Run()
}

func worker(id int, games <-chan Game, results chan<- Result) {
	for game := range games {
		fmt.Println("Worker", id, "started game", game.ID)

		// subscribes to a pubsub channel for the game so that it can listen for the game to finish

		// get results
		// send results to results channel
		results <- Result{GameID: game.ID, Result: result}
	}
}

func (l *Lobby) AddPlayer(player PlayerIdentity) {
	l.PlayerQueue <- player
}

func (l *Lobby) RegisterWorker(worker *Worker) {
	l.workerQueue <- worker
}

func InitLobby(numWorkers int) *Lobby {
	lobby := NewLobby()

	for i := 0; i < numWorkers; i++ {
		workerID := fmt.Sprintf("worker%02d", i+1)
		worker := NewWorker(workerID)
		lobby.RegisterWorker(worker)
	}

	go lobby.Run()
	return lobby
}
