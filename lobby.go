package main

import (
	"sync/atomic"
)

// Define a Lobby type that will manage the waiting players
type Lobby struct {
	PlayerQueue chan PlayerIdentity
	workerQueue chan *Worker
	playerCount int32 // Use an int32 to allow for atomic operations
}

var (
	playersPerWorker = 30
)

// NewLobby creates a new Lobby instance
func NewLobby(numWorkers int) *Lobby {
	return &Lobby{
		PlayerQueue: make(chan PlayerIdentity, playersPerWorker*numWorkers),
		workerQueue: make(chan *Worker, numWorkers),
		playerCount: 0,
	}
}

// Run starts the Lobby's processing loop
func (l *Lobby) Run() {
	for {
		player1 := <-l.PlayerQueue
		player2 := <-l.PlayerQueue
		atomic.AddInt32(&l.playerCount, -2) // Two players have left the lobby

		worker := <-l.workerQueue

		worker.PlayerChan <- player1
		worker.PlayerChan <- player2

		l.workerQueue <- worker
	}
}

// AddPlayer adds a player to the Lobby's queue
func (l *Lobby) AddPlayer(player PlayerIdentity) {
	l.PlayerQueue <- player
	atomic.AddInt32(&l.playerCount, 1)
}

// RegisterWorker registers a worker with the Lobby
func (l *Lobby) AddWorker(worker *Worker) {
	l.workerQueue <- worker
}
