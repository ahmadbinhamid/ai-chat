package migrator

import (
	"database/sql"
	"fmt"
)

// Fresh drops every table in the current database, then re-runs all
// migrations from scratch.
func Fresh(db *sql.DB) error {
	rows, err := db.Query(`
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = DATABASE()
	`)
	if err != nil {
		return fmt.Errorf("could not list tables: %w", err)
	}

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	if _, err := db.Exec(`SET FOREIGN_KEY_CHECKS = 0`); err != nil {
		return fmt.Errorf("could not disable foreign key checks: %w", err)
	}
	for _, t := range tables {
		fmt.Println("dropping:", t)
		if _, err := db.Exec("DROP TABLE IF EXISTS `" + t + "`"); err != nil {
			return fmt.Errorf("could not drop table %s: %w", t, err)
		}
	}
	if _, err := db.Exec(`SET FOREIGN_KEY_CHECKS = 1`); err != nil {
		return fmt.Errorf("could not re-enable foreign key checks: %w", err)
	}

	return Run(db)
}
