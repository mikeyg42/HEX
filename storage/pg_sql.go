package storage

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/jackc/pgx/v4/pgxpool"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to parse connection string: %v\n", err)
		os.Exit(1)
	}

	// Connect to PostgreSQL using a connection pool
	pool, err := pgxpool.ConnectConfig(context.Background(), poolConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to establish a connection pool: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	// read the sql file and execute it
	err = executeSQLFile(pool, "/Users/mikeglendinning/projects/HEX/storage/persistenceLayer.sql")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error executing SQL file: %v\n", err)
		os.Exit(1)
	}

	// Your application logic goes here
}

func executeSQLFile(pool *pgxpool.Pool, filename string) error {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}

	sql := string(content)
	_, err = pool.Exec(context.Background(), sql)
	return err
}

func createRoles(pool *pgxpool.Pool) error {
	_, err := pool.Exec(context.Background(), `
        CREATE ROLE app_read;
        CREATE ROLE app_write;
        CREATE ROLE app_auth;
        -- Further GRANT/REVOKE commands can be added as needed
    `)
	return err
}
