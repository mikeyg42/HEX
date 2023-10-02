package models 

import(
	"time"

)



type EvtData struct {
	Data       interface{}
	Topic      Topic
	Originator string
	TimeStamp  time.Time
}

type Topic struct {
	TopicName string
}

type SliceOfSubscribeChans []chan []byte // an array of all the channels that subscribe to a given topic,





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
	PublishPairing() //done 
	LockPairIntoMatch() //partly done
	CleanupAfterMatch() // need to write
	MatchmakingLoop() //done
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
	AddMoveToMemory()
	UpdateTableNewGameStatus()
	InitializeNewSetOfEntriesToTable()
	FetchCombinedMoveList()
	FetchIndividualMoveList()
	DeleteGameFromMemory()
}
// there will be two of these of course
type PlayerController interface {
	PostMove()
	RequestGamestate()
	Acknowledge()
	Reconnect()
}