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

func isGameAcknowledged(gameID, playerID string) bool {
	ackMutex.Lock()
	defer ackMutex.Unlock()

	acks, ok := acknowledgments[gameID]
	if !ok {
		// If the game is not in the acknowledgments, create a new entry for it
		acknowledgments[gameID] = []string{playerID}
		return false
	}

	// Check if the player has already acknowledged the game
	if acks[0] == playerID || acks[1] == playerID {
		return true
	}

	// Add the player to the acknowledgments for this game
	acknowledgments[gameID] = []string{acks[0], playerID}

	// Both players acknowledged, delete the game entry from the map
	delete(acknowledgments, gameID)
	return true
}


func LockTheGame(d *Dispatcher, lg LockedGame) {
    ackCount := 0
    var wg sync.WaitGroup
    wg.Add(2)

    go func() {
        defer wg.Done()
        if isGameAcknowledged(lg.GameID, lg.Player1.PlayerID) {
            ackCount++
        }
    }()

    go func() {
        defer wg.Done()
        if isGameAcknowledged(lg.GameID, lg.Player2.PlayerID) {
            ackCount++
        }
    }()

    wg.Wait()

    if ackCount == 2 {
        // Both acknowledged, start the game
        newCmd := &LetsStartTheGameCmd{
            GameID:    lg.GameID,
            Player1:   lg.Player1.PlayerID,
            Player2:   lg.Player2.PlayerID,
            Timestamp: time.Now(),
        }
        d.CommandDispatcher(newCmd)
    } else {
        // Players didn't acknowledge, abort the game
        Delete(lg.GameID)
    }
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
        LockTheGame(d, lg) // Start or delete the game based on acknowledgment
    }
}

func checkForPlayerAck(cmd *LetsStartTheGameCmd, lg *LockedGame) bool {
    var wg sync.WaitGroup
    ackCh := make(chan bool, 2)
    ackCount := 0

    checkAndAck := func(playerID string) {
        defer wg.Done()
        if isGameAcknowledged(cmd.GameID, playerID) {
            ackCh <- true
        }
    }

    wg.Add(2)
    go checkAndAck(lg.Player1.PlayerID)
    go checkAndAck(lg.Player2.PlayerID)

    wg.Wait()
    close(ackCh)

    // Count the number of true acknowledgments received
    for ack := range ackCh {
        if ack {
            ackCount++
        }
    }

    // Check if both players acknowledged
    if ackCount == 2 {
        return true
    }

    Delete(cmd.GameID)
    return false
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

		newPlayerChan := make(chan PlayerIdentity)

		// hypothetical function to start waiting for new players over network, etc.
		go waitForNewPlayer(newPlayerChan)
	
		// Add players as they join
		for {
			player := <-newPlayerChan // wait for a new player from the channel
			lobby.AddPlayer(player)
		}
	}

func waitForNewPlayer(newPlayerChan chan PlayerIdentity) {
	for {
		// Here would be logic to wait for a new player over network, etc.
		player := getNewPlayer()
		newPlayerChan <- player // send new player to the channel
	}
}

func getNewPlayer() PlayerIdentity {
	// get the player from the lobby somehow???

	// waiting for a connection from a client,
	return PlayerIdentity{
		PlayerID:       "00000001", // an internally assigned integer to avoid unnecessary propogation of their username 
		Username: 		"kushlord123", // probably will not ultimately be here included....
		CurrentPlayerRank:   10, // example

	}
}
