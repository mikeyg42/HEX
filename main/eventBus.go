package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	hex "github.com/mikeyg42/HEX/models"
)


func main() {
	manager := NewGameEventBusManager()

	lobby := manager.CreateNewGameLobby()

	lobby, ok := hex.LobbyController(lobby)
	if !ok {
		//log error (this is probably a panic in dev)
		return
	}
	ctx := context.Background()

	// lobby starts listening for matchmaking events and broadcasting them to the players
	go MatchmakingLoop(lobby, ctx)

	// to create a new GameEventBus instances

	game := manager.createAndRegisterNewGameBus(ctx)
	// wait for the signal...
	game.Shutdown()

	// TBD

}

// manager code

type GameEventBusManager struct {
	MapGameBuses map[string]*GameEventBus
	Mu           sync.RWMutex
}

func NewGameEventBusManager() *hex.GameEventBusManager {
	return &hex.GameEventBusManager{
		MapGameBuses: make(map[string]*hex.GameEventBus),
	}
}

func (manager *hex.GameEventBusManager) createAndRegisterNewGameBus(ctx context.Context) *hex.GameEventBus {

	newGameID := generateUniqueGameID()
	ctx, cancelFn := context.WithCancel(ctx)
	gameBus := &hex.GameEventBus{
		gameID:                  newGameID,
		allSubscriberChannels:   createAllGameChannels(),
		subscribersForEachTopic: make(map[hex.Topic]hex.SliceOfSubscribeChans),
		eventChan:               make(chan hex.EvtData),
		context:                 ctx,
		cancelFn:                cancelFn,
	}

	gameBus.DefineTopicSubscriptions(CreateTopics())

	manager.Mu.Lock()
	// add new gameBus to the map of all gameBuses
	manager.mapGameBuses[newGameID] = gameBus
	manager.Mu.Unlock()

	go gameBus.marshalAndForward(gameBus.eventChan)

	return gameBus
}

func CreateTopics() []hex.Topic {
	return []hex.Topic{
		hex.Topic{TestTopic},
		hex.Topic{TimerTopic},
		hex.Topic{GameLogicTopic},
		hex.Topic{MetagameTopic},
		hex.Topic{ResultsTopic},
	}
}

func (manager *hex.GameEventBusManager) FetchGameBus(gameID string) (*hex.GameEventBus, bool) {
	manager.Mu.RLock()
	defer manager.Mu.RUnlock()

	gameBus, found := manager.mapGameBuses[gameID]
	return gameBus, found
}

func generateUniqueGameID() string {
	//generate uuid of format e.g. 53aa35c8-e659-44b2-882f-f6056e443c99

	// uuid.Must returns the uuis if err== nil, otherwise it panics
	gameIDuuid := uuid.Must(uuid.NewRandom()) // returns a v4 uuid -- generates a random, unique string that is 16 characters long
	gameID := gameIDuuid.String()

	if gameID == "" {
		// if gameID is empty string then input to String() was invalid
		panic(fmt.Errorf("gameID is invalid uuid"))
	}

	return gameID
}

func createAllGameChannels() map[string]chan []byte {

	var folksAttendingGame = []string{"playerA", "playerB", "referee", "timer", "lobby", "memory"}
	allGameChannels := make(map[string]chan []byte)

	for _, person := range folksAttendingGame {
		ch := make(chan []byte)
		allGameChannels[person] = ch
	}

	return allGameChannels
}

type MatchmakingTask struct {
	MatchData      []byte
	PlayerChannels map[string]chan []byte // a mapping of player IDs to their channels
	Process        func([]byte, map[string]chan []byte) error
}
type Pool struct {
	MatchmakingTasks chan MatchmakingTask
}

func (manager *hex.GameEventBusManager) CreateRefereePool(maxGoroutines int) *RefereePool {
	p := &RefereePool{
		RefTasks: make(chan RefTask),
	}
	for i := 0; i < maxGoroutines; i++ {
		go p.RefRoutine()
	}
	return p
}

func (p *RefereePool) RefRoutine() {
	for task := range p.RefTasks {
		err := task.Process(task.Data) //?????
		if err != nil {
			// Handle the error or send it to a results channel
		}
	}
}

type RefereePool struct {
	RefTasks chan RefTask
}
type RefTask struct {
}

type turnStartTurnTimerON_evt struct {
}

type turnAcceptedTurnTimerOFF_evt struct {
}

type timerExpired_evt struct {
}
