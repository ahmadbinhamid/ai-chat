package handlers

import (
	"errors"

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
// special-case.
type chatDetail struct {
	Chat     *chat.Chat         `json:"chat"`
	Messages []messageWithFiles `json:"messages"`
}

// Get returns the tenant's one chat and its full transcript, with each
// turn's generated files attached so reopening the page still shows every
// past "Generated files" card, not just the most recent one. A tenant that
// hasn't sent a first message yet has no chat row — that's a normal state
// (200 with a null chat), not a 404.
func (h *ChatHandler) Get(c *gin.Context) {
	ch, err := h.chats.GetChatForTenant(c.Request.Context(), TenantID(c), themebuild.ChatType)
	if errors.Is(err, chat.ErrNotFound) {
		httpresponse.OK(c, chatDetail{Chat: nil, Messages: []messageWithFiles{}})
		return
	}
	if err != nil {
		respondErr(c, err)
		return
	}

	messages, err := h.chats.ListMessages(c.Request.Context(), TenantID(c), ch.ID)
	if err != nil {
		respondErr(c, err)
		return
	}
	files, err := h.builder.FilesForChat(c.Request.Context(), ch.ID)
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

	httpresponse.OK(c, chatDetail{Chat: &ch, Messages: withFiles})
}
