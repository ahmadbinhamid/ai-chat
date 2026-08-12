// Command migration is the CLI for creating and applying database
// migrations: create <name> scaffolds a new migration file, run applies
// every migration that hasn't run yet, fresh drops every table and re-runs
// them all from scratch. The server itself never auto-migrates — this is
// the only thing that changes the schema, run explicitly (see backend
// README / Makefile).
package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"ai-chat/database/migrator"
	"ai-chat/internal/config"
	"ai-chat/internal/db"

	"github.com/joho/godotenv"

	// Imported for its side effect: each migration file's init() registers
	// itself with the migrator. The migrations package is otherwise unused here.
	_ "ai-chat/database/migrations"
)

type command string

const (
	cmdCreate command = "create"
	cmdFresh  command = "fresh"
	cmdRun    command = "run"
)

const migrationsDir = "database/migrations"

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("no .env file loaded: %v", err)
	}

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "no command given (create <name> | run | fresh)")
		os.Exit(1)
	}
	cmd := command(os.Args[1])

	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "could not create folder:", err)
		os.Exit(1)
	}

	switch cmd {
	case cmdCreate:
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "create needs a table name")
			os.Exit(1)
		}
		create(os.Args[2])
	case cmdFresh:
		fresh()
	case cmdRun:
		run()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", cmd)
		os.Exit(1)
	}
}

func create(tableName string) {
	ts := time.Now().Format("20060102150405") // generate ONCE
	name := ts + "_" + tableName
	filename := name + ".go"

	file, err := os.Create(filepath.Join(migrationsDir, filename))
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not create migration file:", err)
		os.Exit(1)
	}
	defer func() { _ = file.Close() }()

	// The init() makes the file register itself with the runner on import.
	content := fmt.Sprintf(`package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: %q,
		Up:   Up_%s,
		Down: Down_%s,
	})
}

func Up_%s(db *sql.DB) error {
	_, err := db.Exec()
	return err
}

func Down_%s(db *sql.DB) error {
	_, err := db.Exec()
	return err
}
`, name, ts, ts, ts, ts)

	if _, err := file.WriteString(content); err != nil {
		fmt.Fprintln(os.Stderr, "could not write migration file:", err)
		os.Exit(1) //nolint:gocritic // process exit reclaims file's fd either way
	}
	fmt.Println("created:", filepath.Join(migrationsDir, filename))
}

func run() {
	conn := connect()
	defer func() { _ = conn.Close() }()

	if err := migrator.Run(conn); err != nil {
		fmt.Fprintln(os.Stderr, "migration failed:", err)
		os.Exit(1) //nolint:gocritic // process exit reclaims conn's fd either way
	}
}

func fresh() {
	conn := connect()
	defer func() { _ = conn.Close() }()

	if err := migrator.Fresh(conn); err != nil {
		fmt.Fprintln(os.Stderr, "fresh failed:", err)
		os.Exit(1) //nolint:gocritic // process exit reclaims conn's fd either way
	}
}

func connect() *sql.DB {
	conn, err := db.Connect(config.Load())
	if err != nil {
		fmt.Fprintln(os.Stderr, "db connection failed:", err)
		os.Exit(1)
	}
	return conn
}
