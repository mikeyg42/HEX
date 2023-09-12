package pregame

import (
	"fmt"
	"sync"
	"time"

	hex "github.com/mikeyg42/HEX/models"
)

const (
	maxWorkers        = 5
	maxQueueSize      = 100
	shutdownThreshold = 3
	initialNumWorkers = 3
)

func NewLobby() *Lobby {
	return &Lobby{
		Games:   make(chan Game, 0),
		Results: make(chan Result, 0),
	}
}

func (l *Lobby) Run() {
	var wg sync.WaitGroup

	// Create initial workers
	for w := 1; w <= initialNumWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			worker(workerID, l.Games, l.Results)
			wg.Done()
		}(w)
	}

	numWorkers := initialNumWorkers
	// Monitor the Games channel length
	go func() {
		for range time.Tick(time.Second) {
			queueSize := len(l.Games)
			if queueSize > maxQueueSize && numWorkers < maxWorkers {
				// Increase the number of workers
				numWorkers++

				fmt.Println("Increasing the number of workers to", numWorkers)
				wg.Add(1)

				go func(workerID int) {
					worker(workerID, l.Games, l.Results)
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

	// Collect Results
	go func() {
		for r := range l.Results {
			fmt.Println("Game", r.GameID, "result:", r.Result)
		}
	}()

	// Assign Games to workers
	// This can be populated by your matchmaking logic
	/*     for j := 1; j <= numGames; j++ {
	       l.Games <- Game{ID: j, Data: j}
	   } */

	// Graceful shutdown
	<-waitForShutdown
	close(l.Results)
	fmt.Println("Worker pool shutdown")
}

func main() {
	lobby := NewLobby()
	go lobby.Run()
}

func StartWorker(con *hex.Container, id int, Games <-chan Game, Results chan<- Result) {
	w := new(Worker) // create a new worker

	for game := range Games {
		fmt.Println("Worker", id, "started game", game.ID)

		w.runGame(game, con)

		// TODO: Fetch the result from the game execution
		// result := ...
		
		Results <- Result{GameID: game.ID, Result: result}
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
 	go lobby.Run()
	return lobby
}
