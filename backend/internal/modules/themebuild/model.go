// Package themebuild stages proposed theme files; ApplyDraft writes, DiscardDraft discards.
package themebuild

import (
	"time"

	"ai-chat/internal/themefs"
)

type FileAction string

const (
	FileActionCreate FileAction = "create"
	FileActionUpdate FileAction = "update"
)

// Distinguishes proposed files from implicit layout splices; audited when writes deferred.
type GeneratedFileKind string

const (
	GeneratedFileKindProposed GeneratedFileKind = "proposed"
	GeneratedFileKindLayout   GeneratedFileKind = "layout"
)

// Audits one staged file; only persistent copy until ApplyDraft writes it.
type GeneratedFile struct {
	ID              string            `json:"id"`
	MessageID       string            `json:"message_id"`
	ChatID          string            `json:"chat_id"`
	FilePath        string            `json:"file_path"`
	Action          FileAction        `json:"action"`
	Kind            GeneratedFileKind `json:"kind"`
	Language        string            `json:"language"`
	Content         string            `json:"content"`
	PreviousContent *string           `json:"previous_content"`
	// pages.json fields; persisted because model's PageRegistryEntry is gone by Apply.
	PageMeta  *themefs.PageMeta `json:"page_meta"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}
