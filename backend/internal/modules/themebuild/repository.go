package themebuild

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/themefs"
)

var ErrNotFound = errors.New("record not found")

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// encodePageMeta/decodePageMeta convert PageMeta to/from the JSON this
// table's page_meta column stores it as — a nil PageMeta encodes to a nil
// []byte, which the MySQL driver writes as a real SQL NULL, not the string
// "null" (see database/sql's handling of a nil []byte parameter).
func encodePageMeta(m *themefs.PageMeta) ([]byte, error) {
	if m == nil {
		return nil, nil
	}
	return json.Marshal(m)
}

func decodePageMeta(raw sql.NullString) (*themefs.PageMeta, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var m themefs.PageMeta
	if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Repository) CreateFile(ctx context.Context, f GeneratedFile) error {
	pageMeta, err := encodePageMeta(f.PageMeta)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO chat_generated_files
			(id, message_id, chat_id, file_path, action, kind, language, content, previous_content, page_meta, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, f.ID, f.MessageID, f.ChatID, f.FilePath, f.Action, f.Kind, f.Language, f.Content, f.PreviousContent, pageMeta, f.CreatedAt, f.UpdatedAt)
	return err
}

// generatedFileColumns is the column list scanGeneratedFile expects, in
// order — shared by every SELECT in this file for the same reason
// generation.go's generationColumns is: one place a column can't be added
// to one query and silently missed by another's positional Scan.
const generatedFileColumns = `
	id, message_id, chat_id, file_path, action, kind, language, content, previous_content, page_meta, created_at, updated_at
`

func scanGeneratedFile(row interface{ Scan(dest ...any) error }) (GeneratedFile, error) {
	var f GeneratedFile
	var pageMeta sql.NullString
	err := row.Scan(&f.ID, &f.MessageID, &f.ChatID, &f.FilePath, &f.Action, &f.Kind, &f.Language, &f.Content,
		&f.PreviousContent, &pageMeta, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return GeneratedFile{}, err
	}
	f.PageMeta, err = decodePageMeta(pageMeta)
	if err != nil {
		return GeneratedFile{}, err
	}
	return f, nil
}

// ListFilesByChat returns every generated file ever written in a chat, in
// one query — used to hydrate a chat's full history (GET /chat) without an
// N+1 query per message. Relies on chat_id being denormalized onto
// chat_generated_files for exactly this reason.
func (r *Repository) ListFilesByChat(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+generatedFileColumns+`
		FROM chat_generated_files WHERE chat_id = ? ORDER BY created_at ASC
	`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []GeneratedFile
	for rows.Next() {
		f, err := scanGeneratedFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// DraftFiles returns the chat's unsaved overlay: latest content per
// file_path across every generated-file row whose message is still
// apply_status = 'pending'. Later turns win — a merchant's second prompt
// editing a file the first prompt just created should read (and the draft
// should show) the second prompt's version. kind='layout' rows are
// included here (unlike PendingGeneration-facing lists elsewhere): the
// overlay is about file CONTENT for reading, and a turn that spliced a new
// <link> into layout-start.liquid needs later turns' reads of that path to
// see the spliced draft version, not the stale saved one — otherwise a
// third turn's own layout splice would compute its diff against the wrong
// "current" content and could silently drop the second turn's link.
//
// Ordering is (m.created_at, f.created_at, f.id) — id is the tie-break for
// the same reason DequeueNext's ORDER BY needs one (see generation.go):
// created_at is a DATETIME with only second-level precision, and two turns
// (or two files within one turn) landing in the same wall-clock second
// would otherwise tie non-deterministically. revert.go already documents
// this exact hazard for created_at ordering; this does not reintroduce it.
func (r *Repository) DraftFiles(ctx context.Context, chatID string) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT f.file_path, f.content
		FROM chat_generated_files f
		JOIN chat_messages m ON m.id = f.message_id
		WHERE f.chat_id = ? AND m.apply_status = ?
		ORDER BY m.created_at, f.created_at, f.id
	`, chatID, string(chat.ApplyStatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	draft := make(map[string]string)
	for rows.Next() {
		var path, content string
		if err := rows.Scan(&path, &content); err != nil {
			return nil, err
		}
		draft[path] = content // later rows (later turns) overwrite earlier ones
	}
	return draft, rows.Err()
}

// PendingFiles returns every generated-file row (proposed and layout alike
// — see GeneratedFileKind) belonging to a still-'pending' message, oldest
// first — what Service.ApplyDraft folds into a writePlan. Same ordering
// rationale as DraftFiles.
func (r *Repository) PendingFiles(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+generatedFileColumnsPrefixed("f")+`
		FROM chat_generated_files f
		JOIN chat_messages m ON m.id = f.message_id
		WHERE f.chat_id = ? AND m.apply_status = ?
		ORDER BY m.created_at, f.created_at, f.id
	`, chatID, string(chat.ApplyStatusPending))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []GeneratedFile
	for rows.Next() {
		f, err := scanGeneratedFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// generatedFileColumnsPrefixed is generatedFileColumns with every column
// qualified by alias — needed once DraftFiles/PendingFiles' queries join
// chat_messages, whose own columns (id, created_at, ...) would otherwise
// collide with chat_generated_files' identically-named ones.
func generatedFileColumnsPrefixed(alias string) string {
	cols := []string{"id", "message_id", "chat_id", "file_path", "action", "kind", "language", "content", "previous_content", "page_meta", "created_at", "updated_at"}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += alias + "." + c
	}
	return out
}

// MarkMessagesApplied stamps every still-'pending' message in chatID as
// 'applied' with AppliedAt = at — called once, after Service.ApplyDraft has
// successfully written every pending file to the real theme. Deliberately
// chat-wide, not per-message: a draft is applied as a whole (see
// chat.ApplyStatus's doc comment), there is no partial-apply concept.
func (r *Repository) MarkMessagesApplied(ctx context.Context, chatID string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages SET apply_status = ?, applied_at = ?
		WHERE chat_id = ? AND apply_status = ?
	`, string(chat.ApplyStatusApplied), at, chatID, string(chat.ApplyStatusPending))
	return err
}

// ListAppliedFilesByChat returns every generated-file row belonging to an
// 'applied' message, oldest first — what revertAppliedHistory computes
// "what does the live theme currently look like" from. Deliberately
// excludes 'pending'/'discarded' rows: those were never written to
// FlowPOS, so they say nothing true about the live theme's history.
func (r *Repository) ListAppliedFilesByChat(ctx context.Context, chatID string) ([]GeneratedFile, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+generatedFileColumnsPrefixed("f")+`
		FROM chat_generated_files f
		JOIN chat_messages m ON m.id = f.message_id
		WHERE f.chat_id = ? AND m.apply_status = ?
		ORDER BY f.created_at ASC
	`, chatID, string(chat.ApplyStatusApplied))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []GeneratedFile
	for rows.Next() {
		f, err := scanGeneratedFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// DiscardMessagesAfter marks every still-'pending' message in chatID
// created after `after` as 'discarded', returning the distinct
// (non-layout — see GeneratedFileKind) file paths those messages had
// staged, for revertWithinDraft's RevertResult. The SELECT runs before the
// UPDATE deliberately: once a message is marked 'discarded' its rows drop
// out of anything scoped to apply_status = 'pending', including a query
// trying to report what just got discarded.
func (r *Repository) DiscardMessagesAfter(ctx context.Context, chatID string, after time.Time) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT f.file_path
		FROM chat_generated_files f
		JOIN chat_messages m ON m.id = f.message_id
		WHERE f.chat_id = ? AND m.apply_status = ? AND m.created_at > ? AND f.kind != ?
	`, chatID, string(chat.ApplyStatusPending), after, string(GeneratedFileKindLayout))
	if err != nil {
		return nil, err
	}
	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			_ = rows.Close()
			return nil, err
		}
		paths = append(paths, p)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	if _, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages SET apply_status = ?
		WHERE chat_id = ? AND apply_status = ? AND created_at > ?
	`, string(chat.ApplyStatusDiscarded), chatID, string(chat.ApplyStatusPending), after); err != nil {
		return nil, err
	}
	return paths, nil
}

// MarkMessagesDiscarded stamps every still-'pending' message in chatID as
// 'discarded' — called by Service.DiscardDraft. Messages themselves are
// never deleted (see chat.ApplyStatusDiscarded's doc comment): the
// transcript should still show a discarded turn happened, just struck
// through/greyed on the frontend, not vanished.
func (r *Repository) MarkMessagesDiscarded(ctx context.Context, chatID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE chat_messages SET apply_status = ?
		WHERE chat_id = ? AND apply_status = ?
	`, string(chat.ApplyStatusDiscarded), chatID, string(chat.ApplyStatusPending))
	return err
}
