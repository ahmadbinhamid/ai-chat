// Package db owns the pooled MySQL connection.
package db

import (
	"database/sql"
	"fmt"
	"time"

	"ai-chat/internal/config"

	_ "github.com/go-sql-driver/mysql"
)

// Connect opens a pooled MySQL connection from the given config and pings it.
func Connect(cfg config.Config) (*sql.DB, error) {
	conn, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}

	conn.SetMaxOpenConns(25)
	conn.SetMaxIdleConns(5)
	conn.SetConnMaxLifetime(5 * time.Minute)

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return conn, nil
}
