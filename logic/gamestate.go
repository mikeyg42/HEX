package logic

import (
	hex "github.com/mikeyg42/HEX/models"
	// "github.com/mikeyg42/HEX/retry"

	// timer "github.com/mikeyg42/HEX/timerpkg"
	zap "go.uber.org/zap"
)

const SideLenGameboard = 15
const maxRetries = 3

type GameController struct {
	WorkerID  string
	MsgChan   chan interface{} // this chan connects the worker to players/front end, tells it when to start a game and it recieved signal that game is over
	Persister *hex.GameStatePersister
	Errlog    *zap.Logger
	GameID    string
	//	Timer      *timer.TimerControl
	Id_PlayerA string
	Id_PlayerB string
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
