package storage

import (
	"context"
	"fmt"
	"os"
	"time"
	"crypto/tls"
	"errors"

	pgx "github.com/jackc/pgx/v5"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	SQL_namedPreparedStmts "github.com/mikeyg42/HEX/storage/sqlconstants"
	pgxUUID "github.com/vgarvardt/pgx-google-uuid/v5"
)
const maxReadPoolSize = 10
const maxWritePoolSize = 10
const readTimeout = 2 * time.Second
const writeTimeout = 2 * time.Second
const maxRetries = 3
const retryDelay = 500 * time.Millisecond

type pooledConnections struct {
	poolConfig     *pgxpool.Config
	maxReadPoolSize  int
	maxWritePoolSize int
	readTimeout   time.Duration
	writeTimeout  time.Duration
	readPool      *pgxpool.Pool
	writePool     *pgxpool.Pool
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
	pool.InitializeConnPools(ctx)
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
		panic(fmt.Errorf(os.Stderr, "Unable to parse connection string: %v\n", err))
	}

	// Configure TLS 
	poolConfig.ConnConfig.TLSConfig = &tls.Config{
		ServerName: " 0.0.0.0"  , //???,
		InsecureSkipVerify: false, //???,
	}

	// see this re: prepared stateents https://github.com/jackc/pgx/issues/791#issuecomment-660508309
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		// provide type support for google/uuid for your postgresql driver by registering the typemap
		pgxUUID.Register(conn.PgConn().TypeMap())
		// prepare statements
		return makeAvail_NamedPreparedStatements(ctx, conn)
	}
		
	return pooledConnections{
		poolConfig:    poolConfig,
		maxReadPoolSize:  maxReadPoolSize,
		maxWritePoolSize: maxWritePoolSize,
		readTimeout:   time.Duration(readTimeout),
		writeTimeout:  time.Duration(writeTimeout),
	}, nil
}


func makeAvail_NamedPreparedStatements(ctx context.Context, conn *pgx.Conn) error {
	var prepStmts map[string]string

	// Define map of statement name to SQL
	prepStmts = map[string]string{
		"FetchOngoingGames": SQL_namedPreparedStmts.fetch_ongoing_games,
		"FetchMovesByGameID": SQL_namedPreparedStmts.Fetch_all_moves_for_gameID,
		"FetchAPlayersMoves": SQL_namedPreparedStmts.Fetch_a_particular_players_moves,
		"Fetch3recentMoves": SQL_namedPreparedStmts.Fetch_latest_three_moves_in_game,
		"AddNewUser": SQL_namedPreparedStmts.AddNewUserFunction,
		"UpdateGameOutcomes": SQL_namedPreparedStmts.Update_game_outcome,
		"AddNewGame": SQL_namedPreparedStmts.Add_new_game,			
		"DeleteGame": SQL_namedPreparedStmts.Delete_game,
		"AddNewMove": SQL_namedPreparedStmts.Add_new_move,
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
		if err != nil  {
			panic(fmt.Errorf("Failed to prepare statement %v after %v retries: %v", name, maxRetries, err))
		} 
	}
	return nil
}

func (p *pooledConnections) InitializeConnPools(ctx context.Context)  {

	// Initialize database connection pools.
	err := p.create2Pools(ctx)
	// this adds the two pools to the struct p
	if err != nil {
		panic(fmt.Errorf("Error initializing connection pools: %v", err))
	}

	errRead := PingPooledConnections(ctx, p.readPool)
	errWrite := PingPooledConnections(ctx, p.writePool)
	if errRead != nil || errWrite != nil {
		panic(errors.Join(errRead, errWrite))
	}
	
	return
}

// check that the pools are working one at a time
func PingPooledConnections(ctx context.Context, p *pooledConnections) error {
	pool:= p.writePool
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
				return false, fmt.Errorf("role %s does not exist", role)
			}
			return false, err
		}
	}

	var currentUser string
	err := pool.QueryRow(context.Background(), "SELECT current_user").Scan(&currentUser)
	if err != nil {
		// handle error
	}

	if currentUser == "app_read" || currentUser == "app_write" {
		// make a dummy game entry
		userID := "dummy-uuid-string"
		ct, err := pool.Exec(context.Background(), "AddNewGame", userID, userID)
		if err != nil {
			panic(fmt.Errorf("Failed to execute prepared statement: %v\n", err))
		}


	// 3. Insert and retrieve dummy data 
	
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

	// Note: Make sure to remove the dummy data afterwards, either in this function or another cleanup function.
	}

	return true, nil

}