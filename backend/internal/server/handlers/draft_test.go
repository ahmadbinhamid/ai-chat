package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"
	"ai-chat/internal/themefs"

	"github.com/gin-gonic/gin"
)

func TestDraftHandler_RejectsUnsafeEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reached := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	r := gin.New()
	r.GET("/chats/:chatId/draft", NewDraftHandler(themebuild.NewService(nil, chat.NewService(nil), nil, themefs.NewStore(ts.URL), nil)).Files)

	for _, entry := range []string{"..%2Fsecret.liquid", "pages%2Fhome.css"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/chats/c1/draft?entry="+entry, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "INVALID_ENTRY") {
			t.Errorf("entry %s: got %d %s, want 400 INVALID_ENTRY", entry, w.Code, w.Body.String())
		}
	}
	if reached {
		t.Error("an invalid entry must be refused before anything reaches FlowPOS")
	}
}
