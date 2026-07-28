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

type MessageStatus string

const (
	MessageStatusCompleted MessageStatus = "completed"
	MessageStatusFailed    MessageStatus = "failed"
)

// ApplyStatus records whether an assistant turn's proposed changes were
// written to the real theme. There is no "pending" state to wait on —
// generation applies its changes immediately (see
// themebuild.Service.Generate) — so this is only ever set once, at the same
// time as everything else on the message.
type ApplyStatus string

const (
	ApplyStatusNotApplicable ApplyStatus = "not_applicable"
	ApplyStatusApplied       ApplyStatus = "applied"
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

// Message is one append-only turn in a Chat. UserName is only ever set on
// user-role turns (see chk_chat_messages_user_role) — an assistant turn has
// no speaker to attribute, it's always shown as the AI.
type Message struct {
	ID           string        `json:"id"`
	ChatID       string        `json:"chat_id"`
	TenantID     uint64        `json:"tenant_id"`
	Role         Role          `json:"role"`
	UserID       *uint64       `json:"user_id"`
	UserName     *string       `json:"user_name"`
	Content      string        `json:"content"`
	Status       MessageStatus `json:"status"`
	ErrorMessage *string       `json:"error_message"`
	InputTokens  int64         `json:"input_tokens"`
	OutputTokens int64         `json:"output_tokens"`
	ApplyStatus  ApplyStatus   `json:"apply_status"`
	AppliedAt    *time.Time    `json:"applied_at"`
	CreatedAt    time.Time     `json:"created_at"`
}
