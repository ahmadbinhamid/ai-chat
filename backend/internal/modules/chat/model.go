// Package chat owns the conversation aggregate: a Chat (thread) and its
// Messages. This is a deliberately generic conversation log — it knows
// nothing about themes, files, or Claude; the AI generation itself, and the
// file artifacts a turn produces, live in the sibling themebuild module,
// which depends on this one (never the other way around) and supplies its
// own "builder" chat Type. That's what keeps this package reusable for a
// future, unrelated chat use case on the same tenant.
package chat

import "time"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// MessageStatus exists for a possible future state beyond "it happened" —
// today most persisted messages are MessageStatusCompleted.
// MessageStatusFailed is the one exception: themebuild.Service.doGenerate
// records one of these for a merchant-visible failure notice when a
// generation errors out, so the chat transcript itself shows *something*
// went wrong (see doGenerate's defer) rather than a WebSocket-only error
// event a merchant who wasn't watching live would never see.
type MessageStatus string

const (
	MessageStatusCompleted MessageStatus = "completed"
	MessageStatusFailed    MessageStatus = "failed"
)

// ApplyStatus records whether an assistant turn's proposed changes have
// been written to the real theme. Generation no longer writes immediately
// (see themebuild/model.go's own package doc comment on the draft/apply
// split) — a turn with changes starts "pending" (generated, previewable via
// the draft overlay, not yet on FlowPOS) and is later resolved to either
// "applied" (themebuild.Service.ApplyDraft) or "discarded"
// (Service.DiscardDraft), both of which stamp every "pending" message in
// the chat at once — a draft is applied/discarded as a whole, there is no
// per-turn apply.
type ApplyStatus string

const (
	ApplyStatusNotApplicable ApplyStatus = "not_applicable"
	ApplyStatusApplied       ApplyStatus = "applied"
	// ApplyStatusPending means this turn proposed changes that exist only
	// in the draft overlay (chat_generated_files rows for this message) —
	// not yet written to FlowPOS.
	ApplyStatusPending ApplyStatus = "pending"
	// ApplyStatusDiscarded means a pending turn's changes were thrown away
	// (Service.DiscardDraft) rather than applied — kept, not deleted, so
	// the transcript still shows the turn happened; see the discarded turn
	// message. AppliedAt is never set for this status.
	ApplyStatusDiscarded ApplyStatus = "discarded"
)

// Chat is the one, ongoing conversation thread for a (tenant_id, type) pair
// — unique in the database, see the 20260727000001 migration. JSON tags are
// snake_case to match the rest of this API (and every other flowPOS
// service) — a struct with no tags would otherwise marshal as PascalCase
// field names, which is what bit this file before a frontend ever consumed it.
type Chat struct {
	ID                string     `json:"id"`
	TenantID          uint64     `json:"tenant_id"`
	Type              string     `json:"type"`
	TotalInputTokens  int64      `json:"total_input_tokens"`
	TotalOutputTokens int64      `json:"total_output_tokens"`
	LastMessageAt     *time.Time `json:"last_message_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// Message is one append-only turn in a Chat. UserName and UserEmail are only
// ever set on user-role turns (see chk_chat_messages_user_role) — an
// assistant turn has no speaker to attribute, it's always shown as the AI.
type Message struct {
	ID           string        `json:"id"`
	ChatID       string        `json:"chat_id"`
	TenantID     uint64        `json:"tenant_id"`
	Role         Role          `json:"role"`
	UserID       *uint64       `json:"user_id"`
	UserName     *string       `json:"user_name"`
	UserEmail    *string       `json:"user_email"`
	Content      string        `json:"content"`
	Status       MessageStatus `json:"status"`
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	ApplyStatus  ApplyStatus   `json:"apply_status"`
	AppliedAt    *time.Time    `json:"applied_at"`
	CreatedAt    time.Time     `json:"created_at"`
	// Attachments is only ever non-empty on a user-role turn that attached
	// one or more files (images and/or one HTML reference — see the
	// image/HTML-attachment features), stored in chat_message_attachments
	// (see the 20260909000002 migration) rather than per-type columns on
	// this row. Every normal read (ListMessagesByChat, hence GET /chat)
	// populates this with METADATA ONLY — no bytes; see
	// MessageAttachment's own doc comment for why, and for the one caller
	// (themebuild.Service.doGenerate) that fetches actual bytes, and how.
	// Never resurfaced to a DIFFERENT, later turn via toTurns (see
	// themebuild's history-building) — only ever relevant to the turn that
	// attached it.
	Attachments []MessageAttachment `json:"attachments,omitempty"`
}

// AttachmentKind identifies what a chat_message_attachments row holds.
// Adding a new kind (e.g. PDF) is a new value here plus a themebuild
// validation-limit entry — not a migration, not new Message/GenerateInput
// fields, not new scan arguments; that's the point of the table this type
// backs (see the 20260909000002 migration's own doc comment).
type AttachmentKind string

const (
	AttachmentKindImage AttachmentKind = "image"
	AttachmentKindHTML  AttachmentKind = "html"
)

// MessageAttachment is one file attached to a user-role turn's prompt — an
// image (up to maxImagesPerMessage per message) or one reference HTML file
// (see the image/HTML-attachment features). Content carries the
// attachment's raw, decoded bytes (never base64 — see the 20260909000002
// migration's own doc comment) and is populated ONLY by a read that
// explicitly asks for it: Repository.GetAttachmentsContent, called by
// themebuild.Service.doGenerate immediately before it needs to actually
// send this attachment to the model — not by ListMessagesByChat, which
// backs GET /chat and every other transcript read, and returns every other
// field but leaves Content nil. That split is the entire reason this table
// exists apart from chat_messages: a page load pays for filenames and
// sizes, never for a turn's attached bytes.
//
// StorageKey is reserved for future external storage (see the migration's
// own doc comment) — nil on every row today; Content is always what's
// populated instead.
type MessageAttachment struct {
	ID         string         `json:"id"`
	MessageID  string         `json:"-"`
	TenantID   uint64         `json:"-"`
	Kind       AttachmentKind `json:"kind"`
	Filename   string         `json:"filename"`
	MediaType  string         `json:"media_type"`
	SizeBytes  int64          `json:"size_bytes"`
	Checksum   string         `json:"-"`
	Position   int            `json:"position"`
	Content    []byte         `json:"-"`
	StorageKey *string        `json:"-"`
	CreatedAt  time.Time      `json:"-"`
}

// MessageImage is one image attached to an OUTGOING prompt — the wire
// format both the HTTP request body (sendMessageRequest.Images) and
// GenerateInput.Images use. Base64 is raw (no data: URI prefix); this is
// never what's stored or read back — chat.Service decodes it into a
// MessageAttachment's raw Content bytes at write time (see
// buildAttachments), and MessageAttachment is what every read path
// (including doGenerate's re-resolution) deals with from then on.
type MessageImage struct {
	Base64    string `json:"base64"`
	MediaType string `json:"media_type"`
}
