package handlers

import (
	"errors"

	"ai-chat/internal/auth"
	"ai-chat/internal/httpresponse"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// ChatHandler exposes the tenant's one, ongoing chat thread — there is no
// list of chats to choose between (see the 20260728000001 migration).
type ChatHandler struct {
	chats   *chat.Service
	builder *themebuild.Service
}

func NewChatHandler(chats *chat.Service, builder *themebuild.Service) *ChatHandler {
	return &ChatHandler{chats: chats, builder: builder}
}

// messageWithFiles attaches a turn's generated files inline, assembled here since chat.Message doesn't depend on themebuild.
type messageWithFiles struct {
	chat.Message
	GeneratedFiles []themebuild.GeneratedFile `json:"generated_files"`
}

// chatDetail is the chat plus its full message log. Chat is a pointer so a tenant with no chat yet gets the same response shape.
// Generating/GenerationError let the frontend poll a send-message call's outcome, since that endpoint returns before generation finishes.
type chatDetail struct {
	Chat            *chat.Chat         `json:"chat"`
	Messages        []messageWithFiles `json:"messages"`
	Generating      bool               `json:"generating"`
	GenerationError string             `json:"generation_error,omitempty"`
	// Queue is the running generation (if any) plus everything queued behind it, oldest first. Always [], never null.
	Queue []themebuild.PendingGeneration `json:"queue"`
	// PendingChanges reports the unapplied draft, if any. File content lives at GET /chats/:chatId/draft to keep this payload small.
	PendingChanges themebuild.DraftSummaryResult `json:"pending_changes"`
}

// Get returns the tenant's one chat with its full transcript and each turn's generated files. No chat yet is a normal 200 with a null chat, not 404.
func (h *ChatHandler) Get(c *gin.Context) {
	ch, err := h.chats.GetChatForTenant(c.Request.Context(), auth.TenantID(c), themebuild.ChatType)
	if errors.Is(err, chat.ErrNotFound) {
		httpresponse.OK(c, chatDetail{
			Chat: nil, Messages: []messageWithFiles{}, Queue: []themebuild.PendingGeneration{},
			PendingChanges: themebuild.DraftSummaryResult{FilePaths: []string{}},
		})
		return
	}
	if err != nil {
		respondErr(c, err)
		return
	}

	// ch was already tenant-scoped by GetChatForTenant above — no need to
	// pay for a second ownership-verifying query for the same chat.
	messages, err := h.chats.ListMessagesForVerifiedChat(c.Request.Context(), ch.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	files, err := h.builder.FilesForChat(c.Request.Context(), ch.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	queue, err := h.builder.ListPendingGenerations(c.Request.Context(), ch.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	pendingChanges, err := h.builder.DraftSummary(c.Request.Context(), ch.ID)
	if err != nil {
		respondErr(c, err)
		return
	}

	filesByMessage := make(map[string][]themebuild.GeneratedFile, len(messages))
	for _, f := range files {
		filesByMessage[f.MessageID] = append(filesByMessage[f.MessageID], f)
	}

	withFiles := make([]messageWithFiles, len(messages))
	for i, m := range messages {
		withFiles[i] = messageWithFiles{Message: m, GeneratedFiles: filesByMessage[m.ID]}
	}

	generating, genErr := h.builder.GenerationStatus(c.Request.Context(), ch.ID)
	if queue == nil {
		queue = []themebuild.PendingGeneration{}
	}
	httpresponse.OK(c, chatDetail{
		Chat: &ch, Messages: withFiles, Generating: generating, GenerationError: genErr,
		Queue: queue, PendingChanges: pendingChanges,
	})
}

// chatStatus is a lightweight poll response so the frontend's WebSocket-fallback poll isn't re-fetching the full transcript every few seconds.
type chatStatus struct {
	Generating      bool   `json:"generating"`
	GenerationError string `json:"generation_error,omitempty"`
}

// Status returns {generating, generation_error} for the tenant's one chat; no chat yet reports generating: false, not 404.
func (h *ChatHandler) Status(c *gin.Context) {
	ch, err := h.chats.GetChatForTenant(c.Request.Context(), auth.TenantID(c), themebuild.ChatType)
	if errors.Is(err, chat.ErrNotFound) {
		httpresponse.OK(c, chatStatus{})
		return
	}
	if err != nil {
		respondErr(c, err)
		return
	}

	generating, genErr := h.builder.GenerationStatus(c.Request.Context(), ch.ID)
	httpresponse.OK(c, chatStatus{Generating: generating, GenerationError: genErr})
}
