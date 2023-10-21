package storage

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	hex "github.com/mikeyg42/HEX"
	SQL_namedPreparedStmts "github.com/mikeyg42/HEX/sqlconstants"
	pgxUUID "github.com/vgarvardt/pgx-google-uuid/v5"
)

var _ persister = &hex.MemoryInterface{}

const maxReadPoolSize = 10
const maxWritePoolSize = 10
const readTimeout = 2 * time.Second
const writeTimeout = 2 * time.Second
const maxRetries = 3
const retryDelay = 500 * time.Millisecond

type pooledConnections struct {
	poolConfig       *pgxpool.Config
	maxReadPoolSize  int
	maxWritePoolSize int
	readTimeout      time.Duration
	writeTimeout     time.Duration
	readPool         *pgxpool.Pool
	writePool        *pgxpool.Pool
}

type Game struct {
	GameID        uuid.UUID `db:"game_id"`
	PlayerAID     uuid.UUID `db:"playerA_id"`
	PlayerBID     uuid.UUID `db:"playerB_id"`
	Outcome       string    `db:"outcome"`
	GameStartTime time.Time `db:"game_start_time"`
}

type Move struct {
	MoveID          int       `db:"move_id"`
	GameID          uuid.UUID `db:"game_id"`
	PlayerID        uuid.UUID `db:"player_id"`
	PlayerCode      string    `db:"player_code"`
	PlayerGameCode  string    `db:"player_game_code"`
	MoveDescription string    `db:"move_description"`
	MoveTime        time.Time `db:"move_time"`
	// Assuming from the latest three moves function
	MoveCounter int `db:"move_counter"`
}

// This is for the fetch_latest_moves_for_game function
type LatestMove struct {
	PlayerGameCode    string `db:"player_game_code"`
	MoveCounter       int    `db:"move_counter"`
	MoveDescription   string `db:"move_description"`
	FormattedMoveTime string `db:"formatted_move_time"`
}

func main() {

	pool, err := definePooledConnections()
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	// now we set up the tables and perissions ...
	initializeDatabaseSchema(ctx, pool.writePool)

	// now we actually finish finalizing the connection pools
	currentUser, success := pool.InitializeConnPools(ctx)
	if success == false {
		time.Sleep(retryDelay)
		currentUser, success = pool.InitializeConnPools(ctx)
		if success == false {
			panic(fmt.Errorf("Unable to initialize connection pools after 1 retry as role: \n", currentUser))
		}
	}

	defer pool.writePool.Close()
	defer pool.readPool.Close()

	go func() {
		// now we need to start waiting for events from the event bus in a goroutine

	}()
}

func initializeDatabaseSchema(ctx context.Context, pool *pgxpool.Pool) error {

	// Define map of statement name to SQL
	initTables := []string{
		SQL_namedPreparedStmts.CreateRoles,
		SQL_namedPreparedStmts.LoadCryptoPkg,
		SQL_namedPreparedStmts.createGameOutcomeEnum,
		SQL_namedPreparedStmts.createTableSQL_moves,
		SQL_namedPreparedStmts.createTableSQL_games,
		SQL_namedPreparedStmts.createTableSQL_users,
		SQL_namedPreparedStmts.Revoke,
		SQL_namedPreparedStmts.GrantAccess,
		SQL_namedPreparedStmts.GrantExecute,
	}

	// Start a new transaction
	tx, err := pool.Begin(ctx)
	if err != nil {
		err := pool.Ping(ctx)
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() { // unsure if this is actually necessary but its safe
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// Execute each SQL statement within the transaction
	for _, sql := range initTables {
		_, err := tx.Exec(ctx, sql)
		if err != nil {
			return fmt.Errorf("failed to execute SQL statement in transaction: %w", err)
		}
	}

	// Commit the transaction if all SQL statements executed successfully
	err = tx.Commit(ctx)
	if err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func definePooledConnections() (pooledConnections, error) {

	// Parse the DSN to a pgxpool.Config struct -- DSN will be regular dsn pulled from env
	// e.g. //	user=jack password=secret host=pg.example.com port=5432 dbname=mydb sslmode=verify-ca pool_max_conns=10

	databaseDSN := os.Getenv("DATABASE_DSN")
	poolConfig, err := pgxpool.ParseConfig(databaseDSN)
	if err != nil {
		panic(fmt.Errorf("Unable to parse connection string: %v\n", err))
	}

	// Configure TLS
	poolConfig.ConnConfig.TLSConfig = &tls.Config{
		ServerName:         " 0.0.0.0", //???,
		InsecureSkipVerify: false,      //???,
	}

	// see this re: prepared statements https://github.com/jackc/pgx/issues/791#issuecomment-660508309
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// provide type support for google/uuid for your postgresql driver by registering the typemap
		pgxUUID.Register(conn.PgConn().TypeMap())
		// prepare statements
		return makeAvail_NamedPreparedStatements(ctx, conn)
	}

	poolConfig.BeforeClose = func(c *pgx.Conn) {
		// this should instead be a log!
		fmt.Println("Closed the connection pool to the database!!")
	}

	return pooledConnections{
		poolConfig:       poolConfig,
		maxReadPoolSize:  maxReadPoolSize,
		maxWritePoolSize: maxWritePoolSize,
		readTimeout:      time.Duration(readTimeout),
		writeTimeout:     time.Duration(writeTimeout),
	}, nil
}

func makeAvail_NamedPreparedStatements(ctx context.Context, conn *pgx.Conn) error {
	var prepStmts map[string]string

	// Define map of statement name to SQL
	prepStmts = map[string]string{
		"FetchOngoingGames":  SQL_namedPreparedStmts.fetch_ongoing_games,
		"FetchMovesByGameID": SQL_namedPreparedStmts.Fetch_all_moves_for_gameID,
		"FetchAPlayersMoves": SQL_namedPreparedStmts.Fetch_a_particular_players_moves,
		"Fetch3recentMoves":  SQL_namedPreparedStmts.Fetch_latest_three_moves_in_game,
		"AddNewUser":         SQL_namedPreparedStmts.AddNewUserFunction,
		"UpdateGameOutcomes": SQL_namedPreparedStmts.Update_game_outcome,
		"AddNewGame":         SQL_namedPreparedStmts.Add_new_game,
		"AddNewMove":         SQL_namedPreparedStmts.Add_new_move,
	}

	for name, sql := range prepStmts {
		var err error

		for i := 0; i < maxRetries; i++ {
			// calling Prepare here is how we make the prepared statement available on the connection
			_, err = conn.Prepare(ctx, name, sql)
			if err == nil {
				break // Break if no error
			}

			// Log error

			//wait before retrying
			time.Sleep(retryDelay)
		}
		// If error still present after retries, handle it
		if err != nil {
			panic(fmt.Errorf("Failed to prepare statement %v after %v retries: %v", name, maxRetries, err))
		}
	}
	return nil
}

func (p *pooledConnections) InitializeConnPools(ctx context.Context, pool *pgxpool.Pool) (string, bool) {
	// check permissions first
	var currentUser string
	tf, currentUser, err := checkPermissions(pool)
	if err != nil {
		panic(fmt.Errorf("Error checking permissions: %v", err))
	}
	if tf == false || currentUser != "admin" {
		fmt.Println("incorrect permissions. InitializeConnPools failed")
		return currentUser, false
	}

	// Initialize database connection pools.
	err = p.create2Pools(ctx)
	// this adds the two pools to the struct p
	if err != nil {
		fmt.Println("Error initializing connection pools: %v", err)
		return currentUser, false
	}

	return currentUser, true
}

func checkPermissions(pool *pgxpool.Pool) (bool, string, error) {
	var currentUser string
	err := pool.QueryRow(context.Background(), "SELECT current_user").Scan(&currentUser)
	if err != nil {
		return false, "?", err
		// handle error
	}

	if currentUser == "admin" {
		return true, currentUser, nil
	}

	if strings.Contains(currentUser, "only") {
		// indicates read-only permissions
		return false, currentUser, nil
	}
	fmt.Println("Current user is, %s, is trying to initialize a database connection. shouldnt be allowed!!! ", currentUser)
	return false, currentUser, fmt.Errorf("unexpected user initializing database connection: %s", currentUser)
}

// check that the pools are working one at a time
func PingPooledConnections(ctx context.Context, p *pooledConnections) error {
	pool := p.writePool
	err := pool.Ping(ctx)
	if err != nil {
		err = fmt.Errorf("Unable to ping connection writepool: %v\n", err)
		time.Sleep(retryDelay)
		err2 := pool.Ping(ctx)
		if err2 != nil {
			err = fmt.Errorf("Unable to ping connection readpool after retry: %v\n", err2)
		}
	}
	return err
}

func (p *pooledConnections) create2Pools(ctx context.Context) (err error) {
	//Initialize two connection pools, one for read/write

	p.readPool, err = poolWithMaxSize(ctx, p.poolConfig.Copy(), p.maxReadPoolSize)
	if p.readPool == nil { // if at first you don't succeed, try, try again
		time.Sleep(retryDelay)
		p.readPool, err = poolWithMaxSize(ctx, p.poolConfig.Copy(), p.maxReadPoolSize)
		if err != nil {
			return err
		}
	}

	p.writePool, err = poolWithMaxSize(ctx, p.poolConfig.Copy(), p.maxWritePoolSize)
	if p.writePool == nil { // try again
		time.Sleep(retryDelay)
		p.writePool, err = poolWithMaxSize(ctx, p.poolConfig.Copy(), p.maxWritePoolSize)
		if err != nil {
			return err
		}
	}

	return nil
}

func poolWithMaxSize(ctx context.Context, poolConfig *pgxpool.Config, maxConns int) (pool *pgxpool.Pool, err error) {
	if maxConns != 0 {
		poolConfig.MaxConns = int32(maxConns)
	}

	return pgxpool.NewWithConfig(ctx, poolConfig)
}

func isDatabaseSetupCorrectly(ctx context.Context, p *pooledConnections) (bool, error) {
	pool := p.readPool

	// 1. Check for table existence
	tables := []string{"users", "games", "moves"}
	for _, table := range tables {
		var tablename string
		err := pool.QueryRow(ctx, "SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename = $1;", table).Scan(&tablename)
		if err != nil {
			if err == pgx.ErrNoRows {
				return false, fmt.Errorf("table %s does not exist", table)
			}
			return false, err
		}
	}

	// 2. Check for roles existence
	roles := []string{"app_read", "app_write", "app_auth"}
	for _, role := range roles {
		var rolname string
		err := pool.QueryRow(ctx, "SELECT rolname FROM pg_roles WHERE rolname = $1;", role).Scan(&rolname)
		if err != nil {
			if err == pgx.ErrNoRows {
				return false, fmt.Errorf("role %s does not exist: see pgx.ErrNoRows: %v", role, err)
			}
			return false, err
		}
	}

	// ping connections
	errRead := PingPooledConnections(ctx, p.readPool)
	errWrite := PingPooledConnections(ctx, p.writePool)
	if errRead != nil || errWrite != nil {
		panic(errors.Join(errRead, errWrite))
	}

	// 4. Insert and retrieve dummy data
	// make a dummy game entry
	userID := "dummy-uuid-string"
	ct, err := pool.Exec(context.Background(), "AddNewGame", userID, userID)
	if err != nil {
		panic(fmt.Errorf("Failed to execute prepared statement: %v\n", err))
	}

	ct, err = pool.Exec(ctx, "INSERT INTO users(user_id, username, password_hash) VALUES($1, 'dummyuser', 'dummypassword') ON CONFLICT DO NOTHING;", userID)
	if err != nil {
		return false, fmt.Errorf("failed to insert dummy data into users: %w", err)
	}

	var retrievedID string
	err = pool.QueryRow(ctx, "SELECT user_id FROM users WHERE username = 'dummyuser';").Scan(&retrievedID)
	if err != nil {
		return false, fmt.Errorf("failed to retrieve dummy data from users: %w", err)
	}
	if retrievedID != userID {
		return false, errors.New("retrieved dummy data does not match inserted data")
	}

	return true, nil

}

// uses a transaction to delete from both tables all at once or not at all/
func DeleteGameMemory(ctx context.Context, gameID string, writePool *pgxpool.Pool) error {
	tx, err := writePool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // This will rollback if tx.Commit() isn't called

	// Delete from moves table
	deleteMovesQuery := `DELETE FROM moves WHERE game_id = $1;`
	_, err = tx.Exec(ctx, deleteMovesQuery, gameID)
	if err != nil {
		return err
	}

	// Delete from games table
	deleteGameQuery := `DELETE FROM games WHERE game_id = $1;`
	_, err = tx.Exec(ctx, deleteGameQuery, gameID)
	if err != nil {
		return err
	}

	// Commit the transaction if everything went fine
	err = tx.Commit(ctx)
	if err != nil {
		return err
	}

	return nil
}

func InitiateNewGame(playerA, playerB uuid.UUID, writePool *pgxpool.Pool) (gameID, error) {
	var newGameID uuid.UUID

	// Use the name of the prepared statement, provide the required parameters, and scan the result into newGameID
	err := writePool.QueryRow(context.Background(), "AddNewGame", playerAID, playerBID).Scan(&newGameID)
	if err != nil {
		fmt.Println("Failed to execute prepared statement AddNewGame: %v\n", err)
		return uuid.Nil, err
	}
	fmt.Printf("New game initiated with ID: %s\n", newGameID)
	return newGameID, nil
}

// AddMoveToMemory() adds a move to the moves table
func AddMoveToMemory(move Move, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := pool.QueryRow(context.Background(), "Add_New_Move", move.GameID, move.PlayerIdentifier, move.MoveDescript)

	if err != nil {
		time.Sleep(retryDelay)
		err := pool.QueryRow(context.Background(), "Add_New_Move", move.GameID, move.PlayerIdentifier, move.MoveDescript)

		if err != nil {
			panic(fmt.Errorf("Unable to add move to moves table: %v\n", err))
		}
	}

	return err
}

func FetchLatestThreeMovesInGame(gameID uuid.UUID, readPool *pgxpool.Pool) ([]LatestMove, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := readPool.Query(ctx, "Fetch3recentMoves", gameID)
	if err != nil {
		time.Sleep(retryDelay)
		rows, err = readPool.Query(ctx, "Fetch3recentMoves", gameID)
		if err != nil {
			panic(fmt.Errorf("Unable to fetch latest three moves in game: %v\n", err))
		}
	}
	defer rows.Close()

	var latestMoves []LatestMove // LatestMove is a struct representing a latest move, define it accordingly.
	for rows.Next() {
		var move LatestMove
		err := rows.Scan(&move.PlayerGameCode, &move.MoveCounter, &move.Description, &move.FormattedMoveTime)
		if err != nil {
			return nil, err
		}
		latestMoves = append(latestMoves, move)
	}

	return latestMoves, nil
}

func FetchParticularPlayersMoves(playerGameCode uuid.UUID, readPool *pgxpool.Pool) ([]Move, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := readPool.Query(ctx, "FetchAPlayersMoves", playerGameCode)
	if err != nil {
		time.Sleep(retryDelay)
		rows, err = readPool.Query(ctx, "FetchAPlayersMoves", playerGameCode)
		if err != nil {
			panic(fmt.Errorf("Unable to fetch particular player's moves: %v\n", err))
		}
	}
	defer rows.Close()

	var moves []Move
	for rows.Next() {
		var move Move
		err := rows.Scan(&move.MoveID, &move.GameID, &move.PlayerID, &move.PlayerCode, &move.PlayerGameCode, &move.Description, &move.MoveTime)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move)
	}

	return moves, nil
}

func FetchAllMovesForGameID(gameID uuid.UUID, readPool *pgxpool.Pool) ([]Move, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := readPool.Query(ctx, "fetch_all_moves_for_game", gameID)
	if err != nil {
		time.Sleep(retryDelay)
		rows, err = readPool.Query(ctx, "fetch_all_moves_for_game", gameID)
		if err != nil {
			panic(fmt.Errorf("Unable to fetch all moves for gameID: %v\n", err))
		}
	}
	defer rows.Close()

	var moves []Move
	for rows.Next() {
		var move Move // Move is a struct representing a move, define it accordingly.
		err := rows.Scan(&move.MoveID, &move.GameID, &move.PlayerID, &move.PlayerCode, &move.PlayerGameCode, &move.Description, &move.MoveTime)
		if err != nil {
			return nil, err
		}
		moves = append(moves, move)
	}

	return moves, nil
}

func FetchOngoingGames(gameID, playerID *uuid.UUID, readPool *pgxpool.Pool) ([]Game, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	rows, err := readPool.Query(ctx, "fetch_ongoing_games", gameID, playerID)
	if err != nil {
		time.Sleep(retryDelay)
		rows, err = readPool.Query(ctx, "fetch_ongoing_games", gameID, playerID)
		if err != nil {
			panic(fmt.Errorf("Unable to fetch ongoing games: %v\n", err))
		}
	}
	defer rows.Close()

	var games []Game
	for rows.Next() {
		var game Game // Game is a struct representing a game, define it accordingly.
		err := rows.Scan(&game.GameID, &game.PlayerAID, &game.PlayerBID, &game.Outcome, &game.StartTime)
		if err != nil {
			return nil, err
		}
		games = append(games, game)
	}

	return games, nil
}

func UpdateGameOutcome(gameID uuid.UUID, outcome string, pool *pgxpool.Pool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := pool.Exec(ctx, "update_game_outcome", gameID, outcome)

	if err != nil {
		time.Sleep(retryDelay)
		_, err = pool.Exec(ctx, "update_game_outcome", gameID, outcome)

		if err != nil {
			panic(fmt.Errorf("Unable to update game outcome: %v\n", err))
		}
	}

	return err
}

func AddNewUser(username, passwordHash string, pool *pgxpool.Pool) (uuid.UUID, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var newUserID uuid.UUID
	err := pool.QueryRow(ctx, "add_new_user", username, passwordHash).Scan(&newUserID)

	if err != nil {
		time.Sleep(retryDelay)
		err = pool.QueryRow(ctx, "add_new_user", username, passwordHash).Scan(&newUserID)

		if err != nil {
			panic(fmt.Errorf("Unable to add new user: %v\n", err))
		}
	}

	return newUserID, err
}
