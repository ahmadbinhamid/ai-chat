package migrator

import (
	"database/sql"
	"fmt"
)

// Run applies every migration that hasn't run yet.
func Run(db *sql.DB) error {
	// 1. make sure the tracking table exists
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS migrations (
			name VARCHAR(255) PRIMARY KEY,
			run_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("could not create migrations table: %w", err)
	}

	// 2. load names that already ran into a set
	done := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM migrations`)
	if err != nil {
		return fmt.Errorf("could not read migrations table: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		done[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// 3. run the ones not yet done (All() returns them sorted oldest-first)
	for _, m := range All() {
		if done[m.Name] {
			continue
		}

		fmt.Println("running:", m.Name)
		if err := m.Up(db); err != nil {
			return fmt.Errorf("migration %s failed: %w", m.Name, err)
		}

		if _, err := db.Exec(`INSERT INTO migrations (name) VALUES (?)`, m.Name); err != nil {
			return fmt.Errorf("could not record migration %s: %w", m.Name, err)
		}
	}

	fmt.Println("migrations done")
	return nil
}
