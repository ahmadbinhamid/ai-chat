// Package themebuild is the AI theme builder's actual business logic: it
// calls internal/ai to turn a chat message into proposed file changes,
// stages them into a per-chat draft overlay (see themefs.OverlayStore) and
// records each staged file as a GeneratedFile audit row, but does not write
// to the real theme filesystem at generation time. Writing is a separate,
// explicit step — Service.ApplyDraft — the merchant triggers after
// previewing the draft locally (LiquidJS, on the frontend) across as many
// turns as they like. Service.DiscardDraft throws a draft away instead of
// applying it. It depends on the chat module (for the conversation log)
// rather than the other way around.
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

// GeneratedFileKind distinguishes an audited file the model explicitly
// proposed from the shared layout splices (layout-start.liquid/
// layout-end.liquid) a turn's LayoutLinksToAdd/LayoutScriptsToAdd implicitly
// touch. Both must be audited now that writes are deferred — see the
// 20260813000001 migration's doc comment for why an unaudited layout splice
// used to be safe (immediate write) and silently isn't anymore (deferred
// write, nothing else remembers it until Apply).
type GeneratedFileKind string

const (
	GeneratedFileKindProposed GeneratedFileKind = "proposed"
	GeneratedFileKindLayout   GeneratedFileKind = "layout"
)

// GeneratedFile is an audit record of one file an assistant message staged
// — during the draft/pending window this is the ONLY place its content
// exists outside the model's own reply; it is not written to FlowPOS until
// Service.ApplyDraft. JSON tags are snake_case to match the rest of this
// API — see chat.Chat's comment for why this matters.
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
	// PageMeta is the pages.json fields for a pages/*.liquid file (nil for
	// anything else) — persisted so ApplyDraft can restore it at write time
	// (see themefs.PageMeta and the 20260813000001 migration's doc comment
	// on why this can't just be recomputed later: it comes from the
	// model's PageRegistryEntry, which no longer exists by the time Apply
	// runs).
	PageMeta  *themefs.PageMeta `json:"page_meta"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}
