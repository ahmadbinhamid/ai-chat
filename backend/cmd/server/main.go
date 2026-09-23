// Command server is the ai-chat HTTP API entrypoint. It never auto-migrates the
// schema and drains in-flight requests on SIGINT/SIGTERM before exiting.
package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-chat/internal/config"
	"ai-chat/internal/db"
	"ai-chat/internal/logging"
	"ai-chat/internal/server"

	"github.com/joho/godotenv"
)

// writeTimeout bounds every response. Generation runs in a background goroutine and is
// delivered over WebSocket/polling, not this response, so no route needs a long timeout.
const writeTimeout = 30 * time.Second

func main() {
	if err := godotenv.Load(); err != nil {
		log.Printf("no .env file loaded: %v", err)
	}

	cfg := config.Load()
	logger := logging.New()
	slog.SetDefault(logger)

	conn, err := db.Connect(cfg)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()

	srv, err := server.New(cfg, conn, logger)
	if err != nil {
		logger.Error("server initialization failed", "error", err)
		os.Exit(1) //nolint:gocritic // process exit reclaims conn's fd either way
	}
	defer srv.Close()

	httpServer := &http.Server{
		Addr:              srv.Addr(),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		logger.Info("listening", "addr", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	stop()

	logger.Info("shutting down, draining in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	logger.Info("shutdown complete")
}
