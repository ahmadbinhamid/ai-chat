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

// messageWithFiles attaches a turn's generated files (if any) inline —
// assembled at the handler level since chat.Message deliberately doesn't
// know about themebuild.GeneratedFile (that dependency runs the other way).
type messageWithFiles struct {
	chat.Message
	GeneratedFiles []themebuild.GeneratedFile `json:"generated_files"`
}

// chatDetail is the chat plus its full message log. Chat is a pointer so a
// tenant with no chat yet gets the same shape back (chat: null, messages:
// []) instead of a differently-shaped response the frontend would need to
// special-case. Generating/GenerationError are how the caller learns the
// outcome of a POST /chats/messages call after the fact — that endpoint now
// returns as soon as the prompt is accepted, not once Claude has replied
// (see themebuild.Service.Generate) — so the frontend polls this endpoint
// until Generating flips back to false.
type chatDetail struct {
	Chat            *chat.Chat         `json:"chat"`
	Messages        []messageWithFiles `json:"messages"`
	Generating      bool               `json:"generating"`
	GenerationError string             `json:"generation_error,omitempty"`
	// Queue is the running generation (if any) plus every generation still
	// queued behind it, oldest first — see
	// themebuild.Service.ListPendingGenerations. Always [] rather than null
	// for a chat with nothing pending, same convention as Messages.
	Queue []themebuild.PendingGeneration `json:"queue"`
}

// Get returns the tenant's one chat and its full transcript, with each
// turn's generated files attached so reopening the page still shows every
// past "Generated files" card, not just the most recent one. A tenant that
// hasn't sent a first message yet has no chat row — that's a normal state
// (200 with a null chat), not a 404.
func (h *ChatHandler) Get(c *gin.Context) {
	ch, err := h.chats.GetChatForTenant(c.Request.Context(), auth.TenantID(c), themebuild.ChatType)
	if errors.Is(err, chat.ErrNotFound) {
		httpresponse.OK(c, chatDetail{Chat: nil, Messages: []messageWithFiles{}, Queue: []themebuild.PendingGeneration{}})
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
	httpresponse.OK(c, chatDetail{Chat: &ch, Messages: withFiles, Generating: generating, GenerationError: genErr, Queue: queue})
}

// chatStatus is just the two fields a caller needs to know whether a
// generation is still running — the frontend's WebSocket-unavailable poll
// fallback (see ai-chat-stream.ts's streamGeneration / index.tsx's
// statusQuery) hits this instead of Get so it isn't re-fetching the full
// transcript and every generated file's before/after content every 3s.
type chatStatus struct {
	Generating      bool   `json:"generating"`
	GenerationError string `json:"generation_error,omitempty"`
}

// Status returns {generating, generation_error} for the tenant's one chat.
// A tenant with no chat yet reports generating: false rather than 404,
// mirroring Get's own null-chat handling — there's nothing generating for
// a chat that doesn't exist, that's not an error condition here.
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
