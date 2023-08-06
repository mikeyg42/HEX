package main

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	zap "go.uber.org/zap"
)

var allLockedGames = make(map[string]LockedGame)

func checkForPlayerAck(cmd *LetsStartTheGameCmd, lg LockedGame) bool {
	acked := false

	// Wait for player1's acknowledgement
	time.Sleep(5 * time.Second)
	if isGameAcknowledged(cmd.GameID, lg.Player1.PlayerID) {
		// Player1 acknowledged, proceed to wait for player2's acknowledgement
		time.Sleep(5 * time.Second)
		if isGameAcknowledged(cmd.GameID, lg.Player2.PlayerID) {
			acked = true
		}
	}

	if acked == false {
		Delete(cmd.GameID)
	}

	return acked
}

func Delete(gameID string) {
	lockMutex.Lock()
	delete(allLockedGames, gameID) // Remove the game from the map
	lockMutex.Unlock()
}

// NewWorker function with WorkerID assignment
func NewWorker() *Worker {

	// Generate a unique worker ID (e.g., using UUID)
	workerID := uuid.New().String()

	return &Worker{
		WorkerID:    workerID,
		PlayerChan:  make(chan PlayerIdentity),
		ReleaseChan: make(chan string), // Initialize the broadcast channel
	}
}

// Define the acknowledgment map and mutex
var (
	acknowledgments = make(map[string][]string)
	ackMutex        = sync.Mutex{}
)

// Function to check if both players have acknowledged the game
func isGameAcknowledged(gameID, playerID string) bool {
	ackMutex.Lock()
	defer ackMutex.Unlock()

	// Check if the game is already in the acknowledgment map
	if acks, ok := acknowledgments[gameID]; ok {
		// Check if the player is not already in the acknowledgments
		for _, ackPlayerID := range acks {
			if ackPlayerID == playerID {
				// Player already acknowledged the game
				return true
			}
		}

		// Add the player to the acknowledgments for this game
		acknowledgments[gameID] = append(acks, playerID)

		// Check if both players have acknowledged the game
		if len(acknowledgments[gameID]) == 2 {
			// If both players acknowledged, delete the game entry from the map
			delete(acknowledgments, gameID)
			return true
		}
	} else {
		// If the game is not in the acknowledgments, create a new entry for it
		acknowledgments[gameID] = []string{playerID}
	}

	return false
}

func lockGame(d *Dispatcher, lg LockedGame) {
	gameID := lg.GameID

	lockMutex.Lock()
	_, ok := lg[gameID]
	// add to map if not already there
	if !ok {
		allLockedGames[gameID] = lg
	}
	lockMutex.Unlock()

	// Check if the game is still locked (not claimed by another goroutine)
	if !ok {
		// The game was claimed by another goroutine, abort this instance
		return
	}

	P1 := lg.Player1.PlayerID
	P2 := lg.Player2.PlayerID

	// Wait for player1+2's acknowledgement
	time.Sleep(5 * time.Second)
	if isGameAcknowledged(gameID, P1) {
		time.Sleep(5 * time.Second)
		if isGameAcknowledged(gameID, P2) {
			// both acknowledged, start the game

			newCmd := &LetsStartTheGameCmd{
				GameID:    gameID,
				Player1:   P1,
				Player2:   P2,
				Timestamp: time.Now(),
			}

			d.CommandDispatcher(newCmd)

			return
		}
	}
	// Players didn't acknowledge, abort the game
	Delete(gameID)
	return
}

func (w *Worker) Run(d *Dispatcher) {
	for { // this is an infinite loop to continuously handle incoming players and games.
		// Listen for incoming player identities
		player1 := <-w.PlayerChan
		player2 := <-w.PlayerChan

		w.GameID = uuid.New().String() // Generate a unique WORKER ID (e.g., using UUID)

		// Randomize player order with rand.Shuffle
		players := [2]PlayerIdentity{player1, player2}
		rand.Shuffle(2, func(i, j int) { players[i], players[j] = players[j], players[i] })

		// Lock the game with the two players in a shared map
		lockGame(d, LockedGame{
			GameID:   w.GameID,
			WorkerID: w.WorkerID,
			Player1:  players[0],
			Player2:  players[1],
		})

		// Wait for the release notification
		select {
		case playerID := <-w.ReleaseChan:
			// Release the player if it matches any of the announced players
			if players[0].PlayerID == playerID || players[1].PlayerID == playerID {
				// Release the player and proceed to the next iteration
				continue
			}
		}
	}
}

func (d *Dispatcher) StartWorkerPool(numWorkers int) {
	// Create a new channel to announce games
	announceGameChan := make(chan *GameAnnouncementEvent)

	// Create and start a pool of workers
	workers := make([]*Worker, numWorkers)
	for i := 0; i < numWorkers; i++ {
		workers[i] = NewWorker()
		go workers[i].Run(d)
	}

	// players join buffered game channel via the front end
	var gameChan = make(chan *PlayerIdentity, 2)
	var playerCount int     // Counter to keep track of the number of players joined
	var lastWorkerIndex int // Keep track of the index of the last assigned worker

	lastWorkerIndex = 0
	playerCount = 0

	go func() {
		for {
			select {
			case player := <-gameChan:
				// Use round-robin to select the next worker
				lastWorkerIndex = (lastWorkerIndex + 1) % numWorkers
				workers[lastWorkerIndex].PlayerChan <- player

				playerCount++
				if playerCount == 2 {
					// Announce the game with the locked players in a new goroutine
					go lockGame(d, LockedGame)

					// Close the channel to indicate that no more players should be added
					close(gameChan)

					// the worker knows the gameID because it was set during "Run" function
					gameID := workers[lastWorkerIndex].GameID
				}

			case <-time.After(15 * time.Second):
				// Timeout if the second player doesn't send identity within 15 seconds.
				d.errorLogger.InfoLog(
					context.TODO(),
					"Timeout: Second player didn't send identity within 15 seconds.",
					zap.String("PlayerID", player.PlayerID),
				)
				close(gameChan) // Close the channel to stop waiting for more players
				return
			}
		}
	}()

	go func() {
		for {
			select {
			case announceEvent := <-announceGameChan:
				// Announce the game with the locked players
				d.EventDispatcher(announceEvent)

				// Notify all workers to release players that match the announced game's players
				for _, w := range workers {
					select {
					case w.ReleaseChan <- announceEvent.FirstPlayerID:
					case w.ReleaseChan <- announceEvent.SecondPlayerID:
					default:
						// If the worker's broadcast channel is busy, skip and proceed to the next worker
					}
				}
			}
		}
	}()

	go func() {
		for {
			select {
			case announceEvent := <-announceGameChan:
				// Announce the game with the locked players
				d.EventDispatcher(announceEvent)

				// Notify all workers to release players that match the announced game's players
				for _, w := range workers {
					for {
						select {
						case w.ReleaseChan <- announceEvent.FirstPlayerID:
							break
						case w.ReleaseChan <- announceEvent.SecondPlayerID:
							break
						case <-time.After(time.Second * 15):
							// If the worker's broadcast channel is busy for more than 5 seconds, log and skip
							d.errorLogger.InfoLog("Warning: Worker's broadcast channel was busy for 5 seconds for PlayerID: %s in GameID: %s. Skipping...", player.PlayerID, gameID)
							break
						default:
							time.Sleep(time.Millisecond * 100) // If the channel is busy, wait and try again
							continue
						}
						break
					}
				}
			}
		}
	}()
	return
}
