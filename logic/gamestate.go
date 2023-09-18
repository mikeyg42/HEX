package logic

import (
	"context"
	"sync"

	hex "github.com/mikeyg42/HEX/models"
	"github.com/mikeyg42/HEX/retry"
	storage "github.com/mikeyg42/HEX/storage"
	timer "github.com/mikeyg42/HEX/timerpkg"
	zap "go.uber.org/zap"
	gormlogger "gorm.io/gorm/logger"
)

const SideLenGameboard = 15
const maxRetries = 3

type GameController struct {
	WorkerID   string
	MsgChan    chan interface{} // this chan connects the worker to players/front end, tells it when to start a game and it recieved signal that game is over
	Persister  *hex.GameStatePersister
	Errlog     *logger.Logger
	GameID     string
	Timer      *timer.TimerControl
	Id_PlayerA string
	Id_PlayerB string
}

func (gc *GameController) HandleGameStateUpdate(newMove *hex.GameStateUpdate) interface{} {
	pg := gc.Persister.Postgres
	rs := gc.Persister.Redis

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	// Assuming you have a zap logger instance called `logger`
	logger := storage.InitLogger("path/to/logs/gamestate.log", gormlogger.LogLevel(0))
	ctx = context.WithValue(ctx, "logger", logger)

	playerID := newMove.PlayerID

	moveCounter := newMove.MoveCounter
	gameID := newMove.GameID
	xCoord := newMove.XCoordinate
	yCoord := newMove.YCoordinate

	newVert, err := ConvertToTypeVertex(xCoord, yCoord)
	if err != nil {
		//handle
	}

	// retrieve adjacency matrix and movelist from redis, with postgres as fall back option
	oldAdjG, oldMoveList := storage.FetchGameState(ctx, gameID, playerID, rs, pg)
	newAdjG, newMoveList := IncorporateNewVert(ctx, oldMoveList, oldAdjG, newVert)

	// DONE UPDATING GAMESTATE! NOW WE USE IT
	win_yn := EvalWinCondition(newAdjG, newMoveList)

	// ... AND SAVE IT
	err = storage.PersistGameState_sql(ctx, newMove, pg)
	if err != nil {
		// Handle the error
	}

	var wg sync.WaitGroup
	wg.Add(2) // two persistence actions

	go func() {
		defer wg.Done()
		r := retry.RetryFunc(ctx, func() (string, interface{}, error) {
			err := storage.PersistGraphToRedis(ctx, newAdjG, gameID, playerID, rs)
			return "", nil, err
		})
		if r.Err != nil {
			// Handle the error, possibly by logging it
			logger.Error(ctx, "Failed to persist graph to Redis: %v", zap.Error(r.Err))
		}
	}()

	go func() {
		defer wg.Done()
		r := retry.RetryFunc(ctx, func() (string, interface{}, error) {
			err := storage.PersistMoveToRedisList(ctx, newVert, gameID, playerID, rs)
			return "", nil, err
		})
		if r.Err != nil {
			// Handle the error, possibly by logging it
			logger.Error(ctx, "Failed to persist move List to Redis: %v", zap.Error(r.Err))
		}
	}()

	wg.Wait() // Wait for both persistence actions to complete

	// cancel the context for this event handling
	cancelFunc()

	if win_yn {
		// use the lockedGames map to ascertain who opponent is (ie who's turn is next )
		lg := hex.AllLockedGames[gameID] //check all lockedGames map
		loserID := lg.Player1.PlayerID
		if lg.Player1.PlayerID == playerID {
			loserID = lg.Player2.PlayerID
		}

		newEvt := &hex.GameEndEvent{
			GameID:   gameID,
			WinnerID: playerID,
			LoserID:  loserID,

			WinCondition: "A True Win",
		}
		return newEvt

	} else {
		nextPlayer := "P2"
		if playerID != "P1" {
			nextPlayer = "P1"
		}

		newCmd := &hex.NextTurnStartingCmd{
			GameID:             gameID,
			PriorPlayerID:      playerID,
			NextPlayerID:       nextPlayer,
			UpcomingMoveNumber: moveCounter + 1,
		}
		return newCmd
	}

	// include a check to compare redis and postgres?
}

// ..... CHECKING FOR WIN CONDITION

func EvalWinCondition(adjG [][]int, moveList []hex.Vertex) bool {
	numMoves := len(moveList)
	numRows := numMoves + 2
	numCols := numRows

	// Condition 1: Check if enough tiles to traverse the whole game board
	if numMoves < SideLenGameboard {
		return false
	}

	// Condition 2: Check if at least 1 tile placed in each column
	for i := 0; i < SideLenGameboard; i++ {
		colExists := false
		for _, move := range moveList {
			if move.X == i {
				colExists = true
				break
			}
		}
		if !colExists {
			return false
		}
	}

	thinnedAdj := make([][]int, len(adjG))
	copy(thinnedAdj, adjG)
	thinnedMoveList := make([]hex.Vertex, numMoves)
	copy(thinnedMoveList, moveList)

	for {
		// Find degree 0 and 1 nodes (excluding end points)
		lowDegreeNodes := make([]int, 0)
		for i := 2; i < numRows; i++ {
			degree := 0
			for j := 0; j < numCols; j++ {
				degree += thinnedAdj[i][j]
			}
			if degree == 0 || degree == 1 {
				lowDegreeNodes = append(lowDegreeNodes, i)
			}
		}

		// If there are no degree 0 or 1 nodes, break the loop
		if len(lowDegreeNodes) == 0 {
			return true
		}

		var err error
		thinnedAdj, err = ThinAdjacencyMat(thinnedAdj, lowDegreeNodes)
		if err != nil {
			panic(err)
		}

		// Update adjacency matrix and dimensions
		numRows = len(thinnedAdj)
		numCols = numRows

		// Update move list
		thinnedMoveList = RemoveVertices(thinnedMoveList, lowDegreeNodes)

		// Check if condition 1 and 2 are still met
		if len(thinnedMoveList) < SideLenGameboard {
			return false
		}
		columnSet := make(map[int]bool)
		for _, move := range thinnedMoveList {
			columnSet[move.X] = true
		}
		//... and then here, we iterate over width of gameboard, and any columns not represented in the colunnSet map we change to false
		for i := 0; i < SideLenGameboard; i++ {
			if !columnSet[i] {
				return false
			}
		}
		//If we make it to here then we have not thinned enough, and so we proceed with another iteration of thinning
	}
}
