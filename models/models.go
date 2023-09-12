package models

import (
	"context"
	"time"

	"sync"

	cache "github.com/go-redis/cache/v9"
	pubsub "github.com/mikeyg42/HEX/pubsub"
	redis "github.com/redis/go-redis/v9"
	zapcore "go.uber.org/zap/zapcore"
	_ "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Define a struct to represent a locked game with two players
type LockedGame struct {
	WorkerID      string
	GameID        string
	InitialPlayer PlayerIdentity // until after second turn there won't be a player 1 and 2, because of the swap mechanic
	SwapPlayer    PlayerIdentity
	Player1       PlayerIdentity
	Player2       PlayerIdentity
}

var (
	AllLockedGames = make(map[string]LockedGame) // the key is the gameID, the value is the whole locked game struct
	LockMutex      = sync.Mutex{}
)

type GameStateControllerInterface interface {
	RecreateGS_Postgres(ctx context.Context, playerID, gameID string, moveCounter int) ([][]int, []Vertex, error)
}

type TimerController interface {
	StopTimer()
	StartTimer()
}

type LogController interface {
	ErrorLog(ctx context.Context, msg string, fields ...zapcore.Field)
	InfoLog(ctx context.Context, msg string, fields ...zapcore.Field)
	Sync()
}

type Vertex struct {
	X int `json:"x" gorm:"type:integer"`
	Y int `json:"y" gorm:"type:integer"`
}

type GameState struct {
	gorm.Model
	GameID          string   `gorm:"type:varchar(100);uniqueIndex"`
	Player1Moves    []Vertex `gorm:"type:jsonb"`
	AdjacencyGraph1 [][]int  `json:"adjacencyGraph1" gorm:"type:jsonb"`
	Player2Moves    []Vertex `gorm:"type:jsonb"`
	AdjacencyGraph2 [][]int  `json:"adjacencyGraph2" gorm:"type:jsonb"`
	Persister       *GameStatePersister
	Timer           TimerController
}

type MoveLog struct {
	ID          uint   `gorm:"primaryKey"`
	GameID      string `gorm:"type:varchar(100);index"`
	MoveCounter int
	PlayerID    string `gorm:"type:varchar(100)"`
	XCoordinate string `gorm:"type:varchar(2)"`
	YCoordinate int
	Timestamp   int64 `gorm:"autoCreateTime:milli"`
}

// type Worker_old struct {
// 	WorkerID    string
// 	GameID      string
// 	PlayerChan  chan PlayerIdentity
// 	ReleaseChan chan string // Channel to notify the worker to release the player
// 	// to initiate a game:
// 	StartChan chan Command
// }

// type Lobby_old struct {
// 	PlayerQueue chan PlayerIdentity
// 	WorkerQueue chan *Worker
// 	PlayerCount int32
// 	WorkerCount int32
// }

//.............. PERSISTENCE ....................//

type PostgresGameState struct {
	DB *gorm.DB
	// for logging erors specific to redis and postgres
	Logger LogController
	// for setting up timeouts
	Context    context.Context
	TimeoutDur time.Duration
}

type RedisGameState struct {
	Client  *redis.Client
	MyCache *cache.Cache
	// for logging erors specific to redis and postgres
	Logger LogController
	// for setting up timeouts
	Context    context.Context
	TimeoutDur time.Duration
}

type GameStatePersister struct {
	Postgres *PostgresGameState
	Redis    *RedisGameState
	Cache    *cache.Cache
}

// this struct holds all the dependencies required by other parts of the system. nothing game specific here
type Container struct {
	ErrorLog    LogController
	EventCmdLog LogController
	Persister   *GameStatePersister
	CtxExiterFn *ContextMaster_exiter
}
type ContextMaster_exiter struct {
	ParentCancelFunc context.CancelFunc	
}

// DUPLICATED STRUCT ... ALSO FOUND IN WORKERPOOL
type PlayerIdentity struct {
	PlayerID          string
	CurrentGameID     string
	CurrentOpponentID string
	CurrentPlayerRank int
	Username          string // Player's username (this is their actual username, whereas PlayerID will be a UUID+player1 or UUID+player2)
}

//.............. Logger keys ....................//

type EventCmdLoggerKey struct{}
type ErrorLoggerKey struct{}

type ContextFn func(ctx context.Context) []zapcore.Field

// .................. EXIT ........................//


var CmdTypeMap = map[string]interface{}{
	"DeclaringMoveCmd":    &DeclaringMoveCmd{},
	"LetsStartTheGameCmd": &LetsStartTheGameCmd{},
	"PlayerForfeitingCmd": &PlayerForfeitingCmd{},
	"NextTurnStartingCmd": &NextTurnStartingCmd{},
	"SwapTileCmd":         &SwapTileCmd{},
	"PlayInitialTileCmd":  &PlayInitialTileCmd{},
}

type NextTurnStartingCmd struct {
	pubsub.Command
	GameID             string
	PriorPlayerID      string
	NextPlayerID       string
	UpcomingMoveNumber int
}

type DeclaringMoveCmd struct {
	pubsub.Command
	GameID         string `json:"gameId"`
	SourcePlayerID string `json:"playerId"`
	DeclaredMove   string `json:"moveData"`
}

type CheckForWinConditionCmd struct {
	pubsub.Command
	GameID    string `json:"gameId"`
	MoveCount int    `json:"moveCount"`
	PlayerID  string `json:"playerId"`
}

type LetsStartTheGameCmd struct {
	pubsub.Command
	GameID          string `json:"gameId"`
	NextMoveNumber  int    `json:"nextMoveNumber"`
	InitialPlayerID string `json:"initialPlayerId"`
	SwapPlayerID    string `json:"swapPlayerId"`
}

type PlayerForfeitingCmd struct {
	pubsub.Command
	GameID         string `json:"gameId"`
	SourcePlayerID string `json:"playerId"`
	Reason         string `json:"reason"`
}

type PlayInitialTileCmd struct {
	pubsub.Command
	GameID          string `json:"gameId"`
	InitialPlayerID string `json:"initialplayerId"`
	SwapPlayerID    string `json:"swapplayerId"`
	StartTime       string `json:"starttime"`
}

type SwapTileCmd struct {
	pubsub.Command
	GameID                string `json:"gameId"`
	InitialPlayerID       string `json:"initialplayerId"`
	SwapPlayerID          string `json:"swapplayerId"`
	InitialTileCoordinate string `json:"initialtilecoordinate"`
	StartTime             string `json:"starttime"`
}

// ................ events ......................//
var EventTypeMap = map[string]interface{}{
	"GameAnnouncementEvent":        &GameAnnouncementEvent{},
	"DeclaredMoveEvent":            &DeclaredMoveEvent{},
	"InvalidMoveEvent":             &InvalidMoveEvent{},
	"OfficialMoveEvent":            &OfficialMoveEvent{},
	"GameStateUpdate":              &GameStateUpdate{},
	"GameStartEvent":               &GameStartEvent{},
	"TimerON_StartTurnAnnounceEvt": &TimerON_StartTurnAnnounceEvt{},
	"GameEndEvent":                 &GameEndEvent{},
}

// this event will originate from the matchmaking service and is sent in the GameChan of the lobby and worker
type GameAnnouncementEvent struct {
	pubsub.Event
	PlayerA_ID string `json:"firstplayerID"`
	PlayerB_ID string `json:"secondplayerID"`
}

type DeclaredMoveEvent struct {
	pubsub.Event
	GameID   string `json:"gameId"`
	PlayerID string `json:"playerId"`
	MoveData string `json:"moveData"`
}

type InvalidMoveEvent struct {
	pubsub.Event
	GameID      string `json:"gameId"`
	PlayerID    string `json:"playerId"`
	InvalidMove string `json:"moveData"`
}

type GameStartEvent struct {
	pubsub.Event
	GameID         string
	FirstPlayerID  string
	SecondPlayerID string
}

type TimerON_StartTurnAnnounceEvt struct {
	pubsub.Event
	GameID         string
	ActivePlayerID string
	TurnStartTime  time.Time
	TurnEndTime    time.Time
	MoveNumber     int
}

type OfficialMoveEvent struct {
	pubsub.Event
	GameID   string `json:"gameId"`
	PlayerID string `json:"playerId"`
	MoveData string `json:"moveData"`
}

type GameStateUpdate struct {
	gorm.Model       `redis:"-"`
	pubsub.Event     `redis:"-"`
	GameID           string
	MoveCounter      int
	PlayerID         string
	XCoordinate      string
	YCoordinate      int
	ConcatenatedMove string `redis:"-"`
}

type InitialTilePlayed struct {
	pubsub.Event
	GameID                string `json:"gameId"`
	InitialPlayerID       string `json:"initialplayerId"`
	SwapPlayerID          string `json:"swapplayerId"`
	InitialTileCoordinate string `json:"initialtilecoordinate"`
}

type SecondTurnPlayed struct {
	pubsub.Event
	GameID                string `json:"gameId"`
	InitialPlayerID       string `json:"initialplayerId"`
	SwapPlayerID          string `json:"swapplayerId"`
	InitialTileCoordinate string `json:"initialtilecoordinate"`
	SecondTileCoordinate  string `json:"secondtilecoordinate"`
	Player1               string `json:"player1"`
	Player2               string `json:"player2"`
}

type GameEndEvent struct {
	pubsub.Event
	GameID          string         `json:"gameId"`
	WinnerID        string         `json:"winnerId"`
	LoserID         string         `json:"loserId"`
	WinCondition    string         `json:"moveData"`
	CombinedMoveLog map[int]string `json:"moveLog"`
}
