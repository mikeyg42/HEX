package models

import (
	"time"

	"github.com/google/uuid"
)

const Delimiter = "#"

type EvtData struct {
	EventData      string    `json:"event_type"`
	EventType      string    `json:"event_type"`
	Topic          Topic     `json:"-"`
	GameID         uuid.UUID `json:"game_id"`
	OriginatorUUID uuid.UUID `json:"original_sender"`
	TimeStamp      time.Time `json:"timestamp"`
	EventIDNUM     int       `json:"event_id"`
}

type Topic struct {
	TopicName string
}

type SliceOfSubscribeChans []chan []byte // an array of all the channels that subscribe to a given topic,
// so that when a message is published to that topic, it can be broadcast to all the subscribers

type LobbyEvent struct {
	Data       [2]string
	Originator string
	TimeStamp  time.Time
}

type Player struct {
	PlayerID     string      // other info like the usernae and rank and stuff I will just store elsewhere in a map
	LobbyChannel chan []byte // the player receives on this channel from the lobby
	EventChannel chan []byte // the player broadcasts on this channel to the lobby
}

// -,-,-,-,-,- CAST OF CHARACTERS IN EACH GAME -`-`-`-`-`- \\

type TimerController interface {
	StartTimer()
	StopTimer()
	PublishToTimerTopic()
}

type LobbyController interface {
	PublishPairing()    //done
	LockPairIntoMatch() //partly done
	CleanupAfterMatch() // need to write
	MatchmakingLoop()   //done
}

type Referee interface {
	EvaluateProposedMoveLegality()
	EvaluateWinCondition()
	BroadcastGameEnd()
	BroadcastConnectionFail()
	DemandPlayersAck()
	SignalTimerNextTurnCanStart()
}

type CacheManager interface {
	WriteToCache()
	FetchFromCache()
}

type MemoryInterface interface {
	AddMove_persist()
	CompleteGame_persist() // double check you did not miss any moves and then update the game status
	NewGame_persist()
	FetchMoveList() // can be used to fetch the entire game history of BOTH or JUST 1 player
	DeleteGame_persist()
}

// there will be two of these, of course
type PlayerController interface {
	PostMove()
	RequestGamestate()
	Ack()
	AnnounceConnect() // this needs to happen when players are able to start game. and also after a disconnection
}
