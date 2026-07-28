// Package themebuild is the AI theme builder's actual business logic: it
// calls internal/ai to turn a chat message into proposed file changes and
// writes them straight to the real theme filesystem via internal/themefs,
// recording each write as a GeneratedFile audit row. There is no
// pending/apply split — generation and application happen in the same
// request (see Service.Generate). It depends on the chat module (for the
// conversation log) rather than the other way around.
package themebuild

import "time"

type FileAction string

const (
	FileActionCreate FileAction = "create"
	FileActionUpdate FileAction = "update"
)

// GeneratedFile is an audit record of one file an assistant message wrote.
// JSON tags are snake_case to match the rest of this API — see chat.Chat's
// comment for why this matters.
type GeneratedFile struct {
	ID              string     `json:"id"`
	MessageID       string     `json:"message_id"`
	ChatID          string     `json:"chat_id"`
	FilePath        string     `json:"file_path"`
	Action          FileAction `json:"action"`
	Language        string     `json:"language"`
	Content         string     `json:"content"`
	PreviousContent *string    `json:"previous_content"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}
