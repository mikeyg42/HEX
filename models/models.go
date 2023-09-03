package models

import (
	"context"
	"time"

	cache "github.com/go-redis/cache/v9"
	tm "github.com/mikeyg42/HEX/main"
	lg "github.com/mikeyg42/HEX/storage"
	redis "github.com/redis/go-redis/v9"
	zapcore "go.uber.org/zap/zapcore"
	_ "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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

// Define a struct to represent a locked game with two players
type LockedGame struct {
	WorkerID      string
	GameID        string
	InitialPlayer PlayerIdentity // until after second turn there won't be a player 1 and 2, because of the swap mechanic
	SwapPlayer    PlayerIdentity
	Player1       PlayerIdentity
	Player2       PlayerIdentity
}

// newer= draft of workers
type Worker struct {
	WorkerID string
	GameChan chan Game //

}
type Worker_old struct {
	WorkerID    string
	GameID      string
	PlayerChan  chan PlayerIdentity
	ReleaseChan chan string // Channel to notify the worker to release the player
	// to initiate a game:
	StartChan chan Command
}

type Lobby_old struct {
	PlayerQueue chan PlayerIdentity
	WorkerQueue chan *Worker
	PlayerCount int32
	WorkerCount int32
}

type Game struct {
	ID   int
	Data interface{}
}

type Result struct {
	GameID int
	Result interface{}
}

type Lobby struct {
	Games   chan Game   // this chan will be populated by the matchmaking logic
	Results chan Result // this chan will be populated by the workers relaying the results of the games
}

//.............. PERSISTENCE ....................//

type PostgresGameState struct {
	DB *gorm.DB
	// for logging erors specific to redis and postgres
	Logger lg.Logger
	// for setting up timeouts
	Context    context.Context
	TimeoutDur time.Duration
}

type RedisGameState struct {
	Client  *redis.Client
	MyCache *cache.Cache
	// for logging erors specific to redis and postgres
	Logger lg.Logger
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
type GameContainer struct {
	ErrorLog    lg.Logger
	EventCmdLog lg.Logger
	Persister   *GameStatePersister
	Timer       *tm.TimerControl
	Exiter      *GracefulExit
}

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
type GracefulExit struct {
	ParentCancelFunc context.CancelFunc
}

// ................ commands ......................//
type Event interface {
	// ModifyPersistedDataTable will be used by events to persist data to the database.
	MarshalEvt() ([]byte, error)
}

type Command interface {
	MarshalCmd() ([]byte, error)
}

var CmdTypeMap = map[string]interface{}{
	"DeclaringMoveCmd":    &DeclaringMoveCmd{},
	"LetsStartTheGameCmd": &LetsStartTheGameCmd{},
	"PlayerForfeitingCmd": &PlayerForfeitingCmd{},
	"NextTurnStartingCmd": &NextTurnStartingCmd{},
	"EndingGameCmd":       &EndingGameCmd{},
	"SwapTileCmd":         &SwapTileCmd{},
	"PlayInitialTileCmd":  &PlayInitialTileCmd{},
}

type EndingGameCmd struct {
	Command
	GameID       string `json:"gameId"`
	WinnerID     string `json:"winnerId"`
	LoserID      string `json:"loserId"`
	WinCondition string `json:"moveData"`
}

type NextTurnStartingCmd struct {
	Command
	GameID             string
	PriorPlayerID      string
	NextPlayerID       string
	UpcomingMoveNumber int
	TimeStamp          time.Time
}

type DeclaringMoveCmd struct {
	Command
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	DeclaredMove   string    `json:"moveData"`
	Timestamp      time.Time `json:"timestamp"`
}

type CheckForWinConditionCmd struct {
	Command
	GameID    string    `json:"gameId"`
	MoveCount int       `json:"moveCount"`
	PlayerID  string    `json:"playerId"`
	Timestamp time.Time `json:"timestamp"`
}

type LetsStartTheGameCmd struct {
	Command
	GameID          string    `json:"gameId"`
	NextMoveNumber  int       `json:"nextMoveNumber"`
	InitialPlayerID string    `json:"initialPlayerId"`
	SwapPlayerID    string    `json:"swapPlayerId"`
	Timestamp       time.Time `json:"timestamp"`
}

type PlayerForfeitingCmd struct {
	Command
	GameID         string    `json:"gameId"`
	SourcePlayerID string    `json:"playerId"`
	Timestamp      time.Time `json:"timestamp"`
	Reason         string    `json:"reason"`
}

type PlayInitialTileCmd struct {
	Command
	GameID          string `json:"gameId"`
	InitialPlayerID string `json:"initialplayerId"`
	SwapPlayerID    string `json:"swapplayerId"`
	StartTime       string `json:"starttime"`
}

type SwapTileCmd struct {
	Command
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
}

type GameAnnouncementEvent struct {
	Event
	GameID         string    `json:"gameId"`
	FirstPlayerID  string    `json:"firstplayerID"`
	SecondPlayerID string    `json:"secondplayerID"`
	Timestamp      time.Time `json:"timestamp"`
}

type DeclaredMoveEvent struct {
	Event
	GameID    string    `json:"gameId"`
	PlayerID  string    `json:"playerId"`
	MoveData  string    `json:"moveData"`
	Timestamp time.Time `json:"timestamp"`
}

type InvalidMoveEvent struct {
	Event
	GameID      string `json:"gameId"`
	PlayerID    string `json:"playerId"`
	InvalidMove string `json:"moveData"`
}

type GameStartEvent struct {
	Event
	GameID         string
	FirstPlayerID  string
	SecondPlayerID string
	Timestamp      time.Time
}

type TimerON_StartTurnAnnounceEvt struct {
	Event
	GameID         string
	ActivePlayerID string
	TurnStartTime  time.Time
	TurnEndTime    time.Time
	MoveNumber     int
}

type OfficialMoveEvent struct {
	Event
	GameID    string    `json:"gameId"`
	PlayerID  string    `json:"playerId"`
	MoveData  string    `json:"moveData"`
	Timestamp time.Time `json:"timestamp"`
}

type GameStateUpdate struct {
	gorm.Model       `redis:"-"`
	Event            `redis:"-"`
	GameID           string
	MoveCounter      int
	PlayerID         string
	XCoordinate      string
	YCoordinate      int
	ConcatenatedMove string `redis:"-"`
}

type InitialTilePlayed struct {
	Event
	GameID                string    `json:"gameId"`
	InitialPlayerID       string    `json:"initialplayerId"`
	SwapPlayerID          string    `json:"swapplayerId"`
	InitialTileCoordinate string    `json:"initialtilecoordinate"`
	Timestamp             time.Time `json:"timestamp"`
}

type SecondTurnPlayed struct {
	Event
	GameID                string `json:"gameId"`
	InitialPlayerID       string `json:"initialplayerId"`
	SwapPlayerID          string `json:"swapplayerId"`
	InitialTileCoordinate string `json:"initialtilecoordinate"`
	SecondTileCoordinate  string `json:"secondtilecoordinate"`
	Player1               string `json:"player1"`
	Player2               string `json:"player2"`
	Timestamp             string `json:"timestamp"`
}
