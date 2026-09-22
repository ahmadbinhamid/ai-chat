// Package chat owns the conversation aggregate (Chat, Message) — a generic log knowing
// nothing about themes, files, or the AI provider; those live in the themebuild module.
package chat

import "time"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// MessageStatus records whether a turn completed. MessageStatusFailed lets the transcript
// itself show a generation error, not just a WebSocket-only event a merchant might miss.
type MessageStatus string

const (
	MessageStatusCompleted MessageStatus = "completed"
	MessageStatusFailed    MessageStatus = "failed"
)

// ApplyStatus records whether a turn's changes were written to the real theme. Starts
// "pending" (draft overlay only), then resolves to "applied"/"discarded" for the whole draft at once.
type ApplyStatus string

const (
	ApplyStatusNotApplicable ApplyStatus = "not_applicable"
	ApplyStatusApplied       ApplyStatus = "applied"
	// ApplyStatusPending means changes exist only in the draft overlay, not yet on FlowPOS.
	ApplyStatusPending ApplyStatus = "pending"
	// ApplyStatusDiscarded means a pending turn's changes were thrown away, not deleted —
	// the transcript still shows the turn happened. AppliedAt is never set for this status.
	ApplyStatusDiscarded ApplyStatus = "discarded"
)

// Chat is the one, ongoing conversation thread for a (tenant_id, type) pair, unique in the DB.
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

// Message is one append-only turn in a Chat. UserName/UserEmail are only set on user-role
// turns; an assistant turn has no speaker to attribute.
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
	// Attachments is only non-empty on a user-role turn with files attached. Normal reads
	// populate METADATA ONLY (no bytes) — only doGenerate fetches actual content.
	Attachments []MessageAttachment `json:"attachments,omitempty"`
}

// AttachmentKind identifies what a chat_message_attachments row holds. Adding a new kind
// (e.g. PDF) is just a new value here plus a themebuild validation-limit entry, no migration.
type AttachmentKind string

const (
	AttachmentKindImage AttachmentKind = "image"
	AttachmentKindHTML  AttachmentKind = "html"
)

// MessageAttachment is one file attached to a user-role turn's prompt (an image or one
// reference HTML file). Content holds raw decoded bytes and is populated ONLY by
// Repository.GetAttachmentsContent — normal transcript reads leave it nil, so a page load
// never pays for attached bytes. StorageKey is reserved for future external storage; unused today.
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

// MessageImage is one image attached to an OUTGOING prompt (raw base64, no data: URI prefix).
// Never stored or read back as-is — decoded into a MessageAttachment's Content at write time.
type MessageImage struct {
	Base64    string `json:"base64"`
	MediaType string `json:"media_type"`
}
