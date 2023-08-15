package main

import (
	"context"
	"fmt"
	"sync"
	"time"
	"strings"
	"strconv"

	"github.com/google/uuid"
	redis "github.com/redis/go-redis/v9"
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)// for example, 1 minute timeout
    defer cancel()

    ackCount := 0
    var wg sync.WaitGroup
    wg.Add(2)

	go func() {
        defer wg.Done()
        select {
        case <-ctx.Done():
            return
        default:
            if isGameAcknowledged(lg.GameID, lg.Player1.PlayerID) {
                ackCount++
            }
        }
    }()

	go func() {
        defer wg.Done()
        select {
        case <-ctx.Done():
            return
        default:
            if isGameAcknowledged(lg.GameID, lg.Player2.PlayerID) {
                ackCount++
            }
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
	}
}

func (w *Worker) WorkerRun(d *Dispatcher, l *Lobby) {
	rs := d.persister.redis
	ctx := context.Background()
	
	messageChan, err := w.SubscribeToStream(rs, ctx, l)
    if err != nil {
        fmt.Println("Error setting up subscription to stream:", err)
        return
    }

	for message := range messageChan {
	// Acknowledge the processed message
        _, ackErr := rs.client.XAck(ctx, streamName, l.consumerGroupKey, message.ID).Result()
        if ackErr != nil {
			d.errorLogger.InfoLog(ctx, "Error acknowledging message first time: %v", ackErr)

			for k<6; k++ {
				time.Sleep(100 * time.Millisecond)
				_, err := rs.client.XAck(ctx, streamName, l.consumerGroupKey, message.ID).Result()
				if err==nil {
					ackErr = nil
					break 
				}
			}
		if ackErr != nil {
			d.errorLogger.ErrorLog(ctx, "Error acknowledging message repeatedly: %v", ackErr)
		}

		//interprept messsage
		

    for {
		playerChan := make(chan PlayerIdentity, 2)
		
		player1 := <-playerChan
        player2 := <-playerChan

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
		close(playerChan)
        LockTheGame(d, lg) // Start or delete the game based on acknowledgment

		// Wait for game to end signal

    }
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

var numLobbies int

func (d *Dispatcher)StartNewWorkerPool(ctx context.Context, ctxCancelF context.CancelFunc, numWorkers int) {

	rs := d.persister.redis
	// Create a Lobby
	lobby := NewLobby(numWorkers, numLobbies)
	
	// Create a ConsumerGroup for the Lobby
	rs.createLobbyConsumerGroup(ctx, lobby)

	// Start the Lobby
	go lobby.Run()

	// Create and register workers
	allWorkers := make([]*Worker, numWorkers)
	for i := 0; i < numWorkers; i++ {
		workerID := fmt.Sprintf("worker%02d", i+1)
		allWorkers[i] = NewWorker(workerID)
		go allWorkers[i].WorkerRun(d, lobby) // start the worker's processing loop
		lobby.AddWorker(allWorkers[i])
	}
	
	go lobby.waitForNewPlayer()

	// Add players as they join
	for {
		player := <-lobby.NewPlayerChan // wait for a new player from the channel
		lobby.AddPlayer(player)

		time.Sleep(time.Millisecond * 100)
		
	}
}

func (l *Lobby) waitForNewPlayer() {
	for {
		// Here would be logic to wait for a new player over network, etc.
		player := getNewPlayer()
		l.NewPlayerChan <- player // send new player to the channel
	}
}

func getNewPlayer() PlayerIdentity {
	// get the player from the lobby somehow???

	// waiting for a connection from a client,
	return PlayerIdentity{
		PlayerID:       "00000001", // an internally assigned integer to avoid unnecessary propogation of their username 
		Username: 		"kushlord123", // this will be moved....
		CurrentPlayerRank:   10, // example
	}
}



// STREAM
var streamName = "worker_pool_stream"

func (rs *RedisGameState) makeStreamIfNoneExists(ctx context.Context) error {
    // Use TYPE command to check the type of the key "WorkerPoolStream"
    keyType, err := rs.client.Type(ctx, "WorkerPoolStream").Result()
    if err != nil {
        return fmt.Errorf("error checking stream type: %v", err)
    }

    // If the type is "none", the stream does not exist, so create it
    if keyType == "none" {
        _, err = rs.client.XAdd(ctx, &redis.XAddArgs{
            Stream: "WorkerPoolStream",
            Values: map[string]interface{}{"dummy": "init"},
        }).Result()
        if err != nil {
            return fmt.Errorf("error creating stream: %v", err)
        }
    } else if keyType != "stream" {
        return fmt.Errorf("a key named 'WorkerPoolStream' exists but is not a stream")
    }

    return nil
}
func (rs *RedisGameState) createLobbyConsumerGroup(ctx context.Context, l *Lobby) error {
	
	// new lobby means we need to increase the counter by 1 for the lobby ID
	numLobbies++	
	
	// Create a consumer group (assuming the stream already exists)
	l.consumerGroupKey = "lobby"+strconv.Itoa(l.IDnum)
	err := rs.client.XGroupCreate(ctx,  "gameWorkersGroup",l.consumerGroupKey, "$").Err()
	if err == nil {
        return nil
    }
	   // If we're here, we encountered an error. Check if it's because the consumer group already exists.
	if !strings.Contains(err.Error(), "already exists") {
        return fmt.Errorf("error creating consumer group for lobby!: %v", err)
    }
    
	// Handle collision by incrementing lobby ID
    numLobbies++  // this is a global var for now...
    l.IDnum = numLobbies
    l.consumerGroupKey = "lobby" + strconv.Itoa(l.IDnum)
    
    // Try creating the consumer group again
    err = rs.client.XGroupCreate(ctx, "gameWorkersGroup", l.consumerGroupKey, "$").Err()
    if err == nil {
        return fmt.Errorf("WARN: Lobby ID had to be changed because one already existed. Old ID was %v", l.IDnum-1)
    } 

    return fmt.Errorf("error creating consumer group for lobby after retry!: %v", err)
}

func (w *Worker) SubscribeToStream(rs *RedisGameState, ctx context.Context, l *Lobby) (<-chan redis.XMessage, error) {
    out := make(chan redis.XMessage)

    go func() {
        defer close(out)

        for {
            messages, err := rs.client.XReadGroup(ctx, &redis.XReadGroupArgs{
                Group:    l.consumerGroupKey,
                Consumer: w.WorkerID,
                Streams:  []string{"gameEvents", ">"},
                Block:    5 * time.Second,  // Block for 5 seconds if no messages
                Count:    10,
            }).Result()

            if err != nil {
                fmt.Println("Error reading from stream:", err)
                // Decide how to handle this error. Perhaps add a backoff or sleep before retrying.
                continue
            }

            for _, xMessage := range messages {
                for _, message := range xMessage.Messages {
                    out <- message
                }
            }
        }
    }()

    return out, nil
}
