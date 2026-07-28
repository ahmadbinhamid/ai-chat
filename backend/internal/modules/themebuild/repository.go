package themebuild

import (
	"context"
	"database/sql"
	"errors"
)

var ErrNotFound = errors.New("record not found")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CreateFile(ctx context.Context, f GeneratedFile) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO chat_generated_files
			(id, message_id, chat_id, file_path, action, language, content, previous_content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, f.ID, f.MessageID, f.ChatID, f.FilePath, f.Action, f.Language, f.Content, f.PreviousContent, f.CreatedAt, f.UpdatedAt)
	return err
}

// ListFilesByChat returns every generated file ever written in a chat, in
// one query — used to hydrate a chat's full history (GET /chat) without an
// N+1 query per message. Relies on chat_id being denormalized onto
// chat_generated_files for exactly this reason.
func (r *Repository) ListFilesByChat(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, message_id, chat_id, file_path, action, language, content, previous_content, created_at, updated_at
		FROM chat_generated_files WHERE chat_id = ? ORDER BY created_at ASC
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []GeneratedFile
	for rows.Next() {
		var f GeneratedFile
		if err := rows.Scan(&f.ID, &f.MessageID, &f.ChatID, &f.FilePath, &f.Action, &f.Language, &f.Content,
			&f.PreviousContent, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}
