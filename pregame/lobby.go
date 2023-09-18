package pregame

import (
	"fmt"
	"sync"
	"time"
	"context"

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
		GamesChan:   make(chan hex.MatchmakingEvt, 0),
		ResultsChan: make(chan hex.GameEndEvent, 0),
	}
}

func (l *Lobby) Run(ctx context.Context) {
	var wg sync.WaitGroup

	// USE CONTEXT



	// Create initial workers
	for w := 1; w <= initialNumWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			NewWorker(workerID, l.GamesChan, l.ResultsChan)
			wg.Done()
		}(w)
	}

	numWorkers := initialNumWorkers
	// Monitor the Games channel length
	go func() {
		for range time.Tick(20 * time.Second) {
			queueSize := len(l.Games)
			if queueSize > maxQueueSize && numWorkers < maxWorkers {
				// Increase the number of workers
				numWorkers++

				fmt.Println("Increasing the number of workers to", numWorkers)
				wg.Add(1)

				go func(workerID int) {
					NewWorker(workerID, l.Games, l.Results)
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
		for r := range l.ResultsChan {
			fmt.Println("Game", r.GameID, "result:", r.Result)
		}
	}()

	//Assign Games to workers
	go func() {
		for game := range l.GamesChan {
	       playerA := game.PlayerA_ID
		   playerB :=  game.PlayerB_ID
			// do some stuff
		}
	}()

	// Graceful shutdown
	<-waitForShutdown
	close(l.Results)
	fmt.Println("Worker pool shutdown")
}

func main() {
	lobby := NewLobby()
	
	ctx := context.Background()

	go lobby.Run(ctx)

}

func (l *Lobby) AddPlayer(player PlayerIdentity) {
	l.PlayerQueue <- player
}

func (l *Lobby) AddWorkerToArray(worker *Worker) {
	l.workerQueue <- worker
}

