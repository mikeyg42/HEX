package main

import (
	"context"

	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

// TO DO:

// Confirm that everyone in the code is consistently using zero indexing for the xy coordinates of the board
// you need to include logic to clarify if the adjacency graph and movelist are saved to player 1 or player 2
// you need to figure out how to flip the board for player 2!!!!!

type Vertex struct {
	X int `json:"x" gorm:"type:integer"`
	Y int `json:"y" gorm:"type:integer"`
}

type GameState struct {
	gorm.Model
	Player1Moves    []Vertex `gorm:"type:jsonb"`
	AdjacencyGraph1 [][]int  `json:"adjacencyGraph1" gorm:"type:jsonb"`
	Player2Moves    []Vertex `gorm:"type:jsonb"`
	AdjacencyGraph2 [][]int  `json:"adjacencyGraph2" gorm:"type:jsonb"`
}

const SideLenGameboard = 15

// ..... FETCHING FROM REDIS

func (gsp *GameStatePersister) FetchPlayerMoves(ctx context.Context, gameID, playerID string) ([]Vertex, error) {
	// Construct the key for fetching the list of moves.
	movesKey := fmt.Sprintf("%s:%s:Moves", gameID, playerID)

	// Fetch all moves from Redis using LRange.
	movesJSON, err := gsp.redis.client.LRange(ctx, movesKey, 0, -1).Result()
	if err != nil {
		// Handle the error.
		return nil, err
	}

	// Decode each move from its JSON representation.
	var moves []Vertex
	for _, moveStr := range movesJSON {
		var move Vertex
		err = json.Unmarshal([]byte(moveStr), &move)
		if err != nil {
			// Handle the error. Depending on your application, you might want to continue to the next move or exit early.
			return nil, err
		}
		moves = append(moves, move)
	}

	return moves, nil
}

func (gsp *GameStatePersister) FetchAdjacencyGraph(ctx context.Context, gameID, playerID string) ([][]int, error) {
	// Construct the key for fetching the adjacency graph.
	adjacencyGraphKey := fmt.Sprintf("%s:%s:AdjacencyGraph", gameID, playerID)

	// Fetch the adjacency graph from Redis.
	adjacencyGraphJSON, err := gsp.redis.client.Get(ctx, adjacencyGraphKey).Result()
	if err != nil {
		// Handle the error. It's common for the error to be `redis.Nil` if the key doesn't exist.
		return nil, err
	}

	// Decode the adjacency graph from its JSON representation.
	var adjacencyGraph [][]int
	err = json.Unmarshal([]byte(adjacencyGraphJSON), &adjacencyGraph)
	if err != nil {
		return nil, err
	}

	return adjacencyGraph, nil
}

// ..... UPDATING GAMESTATE WITH A NEW VERT

func (gsp *GameStatePersister) UpdateGS(ctx context.Context, newVert Vertex, gameID, playerID string) ([][]int, []Vertex, error) {

	adjGraph, err := gsp.FetchAdjacencyGraph(ctx, gameID, playerID)
	if err != nil {
		//handle
	}
	moveList, err := gsp.FetchPlayerMoves(ctx, gameID, playerID)
	if err != nil {
		//handle
	}

	// Update Moves
	updatedMoveList := append(moveList, newVert)
	moveCount := len(updatedMoveList)

	// Calculate new size having now appended a new vertex
	sizeNewAdj := moveCount + 2

	// Preallocate/new adjacency graph with initial values copied from the old adjacency graph
	newAdjacencyGraph := make([][]int, sizeNewAdj)
	for i := 0; i < sizeNewAdj; i++ {
		newAdjacencyGraph[i] = make([]int, sizeNewAdj)
	}

	// Copy values from the old adjacency graph
	for i := 0; i < len(adjGraph); i++ {
		copy(newAdjacencyGraph[i], adjGraph[i])
	}

	// Find adjacent vertices by comparing the list of 6 adjacent vertex list to game state move list, if one of the new points neighbors is in the move list, then there is an edge between the new point and that existing point
	sixAdjacentVertices := getAdjacentVertices(newVert)
	for i := 0; i < moveCount; i++ {
		k := i + 2
		for _, potentialEdgePair := range sixAdjacentVertices {
			if containsVert(moveList, potentialEdgePair) {
				// Edge found between new vertex and an existing vertex
				newAdjacencyGraph[k][moveCount] = 1
				newAdjacencyGraph[moveCount][k] = 1
				break // is this right or should it be continue
			}
		}
	}

	// Check if new vertex is in the first column or last column
	if newVert.X == 0 {
		// Edge found between new vertex and the leftmost hidden point
		newAdjacencyGraph[0][sizeNewAdj-1] = 1
		newAdjacencyGraph[sizeNewAdj-1][0] = 1
	} else if newVert.X == SideLenGameboard-1 {
		// Edge found between new vertex and rightmost hidden point
		newAdjacencyGraph[1][sizeNewAdj-1] = 1
		newAdjacencyGraph[sizeNewAdj-1][1] = 1
	}

	return newAdjacencyGraph, updatedMoveList, err
}

func getAdjacentVertices(vertex Vertex) []Vertex {
	return []Vertex{
		{X: vertex.X - 1, Y: vertex.Y + 1},
		{X: vertex.X - 1, Y: vertex.Y},
		{X: vertex.X, Y: vertex.Y - 1},
		{X: vertex.X, Y: vertex.Y + 1},
		{X: vertex.X + 1, Y: vertex.Y},
		{X: vertex.X + 1, Y: vertex.Y - 1},
	}
}

// ..... CHECKING FOR WIN CONDITION

func evalWinCondition(adjG [][]int, moveList []Vertex) bool {
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
	thinnedMoveList := make([]Vertex, numMoves)
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

		thinnedAdj = thinAdjacencyMat(thinnedAdj, lowDegreeNodes)

		// Update adjacency matrix and dimensions
		numRows = len(thinnedAdj)
		numCols = numRows

		// Update move list
		thinnedMoveList = removeVertices(thinnedMoveList, lowDegreeNodes)

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
		//If we make it to here then we have not thinned enough, and so we go forward with the next iteration of thinning
	}
}

// ..... Helper functions

func thinAdjacencyMat(adj [][]int, indices []int) [][]int {
	temp := removeRows(adj, indices)
	temp = transpose(temp)
	thinnedAdj := removeRows(temp, indices)

	// Check for matrix symmetry
	if !isSymmetric(thinnedAdj) {
		// PANIC
	}

	return thinnedAdj
}

func containsInt(items []int, item int) bool {
	for _, val := range items {
		if val == item {
			return true
		}
	}
	return false
}

func containsVert(vertices []Vertex, target Vertex) bool {
	for _, v := range vertices {
		if v.X == target.X && v.Y == target.Y {
			return true
		}
	}
	return false
}

func removeRows(s [][]int, indices []int) [][]int {
	result := make([][]int, 0)
	for i, row := range s {
		if containsInt(indices, i) {
			continue
		}
		newRow := make([]int, 0)
		for j, val := range row {
			if containsInt(indices, j) {
				continue
			}
			newRow = append(newRow, val)
		}
		result = append(result, newRow)
	}
	return result
}

func removeVertices(s []Vertex, indices []int) []Vertex {
	result := make([]Vertex, 0)
	for i, vertex := range s {
		if containsInt(indices, i) {
			continue
		}
		result = append(result, vertex)
	}
	return result
}

func isSymmetric(matrix [][]int) bool {
	rows := len(matrix)
	if rows == 0 {
		return true
	}
	cols := len(matrix[0])

	for i, row := range matrix {
		if len(row) != cols {
			return false
		}
		for j := i + 1; j < cols; j++ {
			if matrix[i][j] != matrix[j][i] {
				return false
			}
		}
	}

	return true
}

func transpose(slice [][]int) [][]int {
	numRows := len(slice)
	if numRows == 0 {
		return [][]int{}
	}
	numCols := len(slice[0])
	result := make([][]int, numCols)
	for i := range result {
		result[i] = make([]int, numRows)
	}
	for i, row := range slice {
		for j, val := range row {
			result[j][i] = val
		}
	}
	return result
}
