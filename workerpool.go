package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

var allLockedGames = make(map[string]LockedGame)

func GenerateUniqueGameID(db *PostgresGameState, w *Worker) (string, error) {
	var gameID string
	for {
		gameID = fmt.Sprintf("%s-%s", uuid.New().String(), w.WorkerID)

		var count int64
		if err := db.DB.Model(&GameStateUpdate{}).Where("game_id = ?", gameID).Count(&count).Error; err != nil {
			return "", fmt.Errorf("failed to check for existing gameID: %w", err)
		}

		if count == 0 {
			// Found an unused gameID, break the loop
			break
		}

		// Add a delay for DB overload
		time.Sleep(time.Millisecond * 100)
	}
	return gameID, nil
}

func Delete(gameID string) {
	lockMutex.Lock()
	delete(allLockedGames, gameID) // Remove the game from the map
	lockMutex.Unlock()
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

func LockTheGame(d *Dispatcher, lg LockedGame) {
	gameID := lg.GameID

	lockMutex.Lock()
	_, ok := allLockedGames[gameID]
	// add to map if not already there
	if !ok {
		allLockedGames[gameID] = lg
	}
	lockMutex.Unlock()

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

func NewWorker(workerID string) *Worker {
	return &Worker{
		GameID:      "",
		WorkerID:    workerID,
		PlayerChan:  make(chan PlayerIdentity, 2),
		ReleaseChan: make(chan string),
	}
}

func (w *Worker) WorkerRun(d *Dispatcher) {
	for {
		player1 := <-w.PlayerChan
		player2 := <-w.PlayerChan

		// Generate a unique game ID
		gameID, err := GenerateUniqueGameID(d.persister.postgres, w)
		if err != nil {
			// Handle the error here
			continue
		}
		w.GameID = gameID

		// Lock the game
		lg := LockedGame{
			GameID:   gameID,
			WorkerID: w.WorkerID,
			Player1:  player1,
			Player2:  player2,
		}
		LockTheGame(d, lg)

		newCmd := &LetsStartTheGameCmd{
			GameID:    gameID,
			Player1:   player1.PlayerID,
			Player2:   player2.PlayerID,
			Timestamp: time.Now(),
		}
		// Check for acknowledgments and delete or start the game as needed
		if checkForPlayerAck(&LetsStartTheGameCmd{GameID: gameID}, lg) {

			d.CommandDispatcher(newCmd)
		} else {
			Delete(gameID)
		}
	}
}

func checkForPlayerAck(cmd *LetsStartTheGameCmd, lg *LockedGame) bool {
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

func (d *Dispatcher) StartWorkerPool(numWorkers int) {
		// Create a Lobby
		lobby := NewLobby(numWorkers)

		// Start the Lobby
		go lobby.Run()
	
		// Create and register workers
		allWorkers := make([]*Worker, numWorkers)
		for i := 0; i < numWorkers; i++ {
			workerID := fmt.Sprintf("worker%02d", i+1)
			allWorkers[i] = NewWorker(workerID)
			go allWorkers[i].WorkerRun(d) // start the worker's processing loop
			lobby.AddWorker(allWorkers[i])
		}
	
		// Add players as they join
		for {
			player := waitForNewPlayer() // hypothetical function to wait for a new player
			lobby.AddPlayer(player)
		}
}
