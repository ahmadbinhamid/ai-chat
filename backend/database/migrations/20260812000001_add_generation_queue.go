package migrations

import (
	"database/sql"

	"ai-chat/database/migrator"
)

func init() {
	migrator.Register(migrator.Migration{
		Name: "20260812000001_add_generation_queue",
		Up:   Up_20260812000001,
		Down: Down_20260812000001,
	})
}

// This migration turns generations from "one running row per chat" into a
// queue: a chat may now have any number of "queued" rows waiting behind at
// most one "running" one (still enforced by uniq_generations_running_chat,
// unchanged by this migration — see 20260730000001).
//
// prompt/theme_slug/mode are what a queued row remembers about the request
// that created it, since DequeueNext may not promote it to running until
// long after that original HTTP request is gone (see
// themebuild.Service.Generate/runGeneration). theme_slug and mode aren't
// mentioned by name in the original generations schema notes, but
// runGeneration needs them for exactly the same reason it needs prompt —
// GenerateInput can't be replayed from a request that no longer exists.
// The bearer token is deliberately NOT one of these columns — a credential
// in a DB column is a credential-at-rest problem; see
// themebuild.pendingTokens' doc comment for where it actually lives.
//
// user_message_id points at the chat_messages row RecordUserMessage already
// wrote when the prompt was accepted, so the transcript can show a queued
// prompt and the eventual runner can attribute the turn. No foreign key:
// this table already has none to chat_messages (see the original
// generations migration), and adding the first one here is a bigger change
// than this migration needs to make.
//
// started_at moves from NOT NULL to NULL: a queued row hasn't started.
// queued_at is new and NULL only for rows never enqueued (existing rows,
// and any future direct StartGeneration seed) — see idx_generations_chat_
// status_queued, which DequeueNext's ORDER BY relies on.
//
// There is no CHECK constraint on `status` to widen here: the original
// migration (20260730000001) never added one — status is a plain
// VARCHAR(20) NOT NULL DEFAULT 'running' with no enum enforcement at the
// DB layer, so "queued"/"cancelled" (well within 20 chars) need no schema
// change to become valid values, only the Go-side constants
// (GenerationStatusQueued/GenerationStatusCancelled) they're compared
// against.
//
// prompt is added as TEXT NOT NULL with no DEFAULT, not the empty-string
// DEFAULT the brief specified: MySQL rejects a literal default on a TEXT/BLOB/JSON
// column outright (error 1101), discovered running this migration against
// this repo's own dev database, not guessed from documentation. Every
// INSERT that touches this table (StartGeneration, EnqueueGeneration) sets
// prompt explicitly, so the column is never actually left to a default in
// practice — the same effective behavior the brief asked for, just not
// expressible as a schema-level DEFAULT for this column type.
func Up_20260812000001(db *sql.DB) error {
	if _, err := db.Exec(`
		ALTER TABLE generations
		ADD COLUMN prompt TEXT NOT NULL AFTER attempts,
		ADD COLUMN user_message_id CHAR(36) NULL AFTER prompt,
		ADD COLUMN theme_slug VARCHAR(255) NOT NULL DEFAULT '' AFTER user_message_id,
		ADD COLUMN mode VARCHAR(20) NOT NULL DEFAULT '' AFTER theme_slug,
		ADD COLUMN queued_at DATETIME NULL AFTER mode,
		MODIFY COLUMN started_at DATETIME NULL
	`); err != nil {
		return err
	}

	// DequeueNext orders by (queued_at, id) on every generation completion,
	// scoped to one chat's queued rows — this index is what keeps that a
	// bounded lookup instead of a per-chat table scan as generations
	// accumulate.
	_, err := db.Exec(`
		CREATE INDEX idx_generations_chat_status_queued ON generations (chat_id, status, queued_at)
	`)
	return err
}

func Down_20260812000001(db *sql.DB) error {
	if _, err := db.Exec(`DROP INDEX idx_generations_chat_status_queued ON generations`); err != nil {
		return err
	}
	_, err := db.Exec(`
		ALTER TABLE generations
		DROP COLUMN prompt,
		DROP COLUMN user_message_id,
		DROP COLUMN theme_slug,
		DROP COLUMN mode,
		DROP COLUMN queued_at,
		MODIFY COLUMN started_at DATETIME NOT NULL
	`)
	return err
}
