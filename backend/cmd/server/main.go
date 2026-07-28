// Command server is the ai-chat HTTP API entrypoint. It never auto-migrates
// the schema (see cmd/migration) and shuts down gracefully on SIGINT/SIGTERM
// — in-flight requests, including a long-running generation call, are given
// a chance to finish rather than being severed by an abrupt process kill.
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

// writeTimeout is generous specifically because POST /chats/messages calls
// Claude synchronously (streamed internally to avoid the SDK's own
// timeout, but still one HTTP round trip end to end) and, since generation
// now applies its changes to the real theme in the same request, writes
// files before responding too. At xhigh effort with adaptive thinking, a
// full page + CSS + JS generation can legitimately take several minutes. A
// short WriteTimeout here would sever a successful generation right before
// the response is written. Must stay <= the frontend's axios timeout
// (ai-chat-client.ts) — that side needs to give this at least as long.
const writeTimeout = 5 * time.Minute

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
	defer conn.Close()

	srv, err := server.New(cfg, conn, logger)
	if err != nil {
		logger.Error("server initialization failed", "error", err)
		os.Exit(1)
	}

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
