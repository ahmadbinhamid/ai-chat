package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type timingFixture struct {
	conn     *sql.DB
	buildSvc *themebuild.Service
	tenantID uint64
	chatID   string
	genID    string
}

// newTimingFixture gives a fresh tenant a chat with one generation, the shape the dashboard reports timing for.
func newTimingFixture(t *testing.T) timingFixture {
	t.Helper()
	conn := openStreamTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := themebuild.NewRepository(conn)
	f := timingFixture{conn: conn, buildSvc: themebuild.NewService(buildRepo, chatSvc, nil, nil, nil), tenantID: uint64(time.Now().UnixNano())}
	c, err := chatSvc.GetOrCreateChat(context.Background(), f.tenantID, themebuild.ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	f.chatID, f.genID = c.ID, uuid.NewString()
	if _, err := buildRepo.EnqueueGeneration(context.Background(), themebuild.Generation{
		ID: f.genID, ChatID: c.ID, TenantID: f.tenantID, Prompt: "p", ThemeSlug: "shop",
	}); err != nil {
		t.Fatalf("EnqueueGeneration failed: %v", err)
	}
	t.Cleanup(func() { _ = buildRepo.CancelQueued(context.Background(), c.ID, f.genID) })
	return f
}

func (f timingFixture) post(t *testing.T, h *ClientTimingHandler, tenantID uint64, chatID, genID, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := gin.New()
	router.Use(fakeAuthMiddleware(tenantID))
	router.POST("/chats/:chatId/generations/:generationId/client-timing", h.Record)
	req := httptest.NewRequest(http.MethodPost, "/chats/"+chatID+"/generations/"+genID+"/client-timing", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

type timingRow struct {
	chatID                                                             string
	tenantID                                                           uint64
	post, wsConnect, firstEvent, firstProgress, completed, previewSeen sql.NullInt64
}

func readTiming(t *testing.T, conn *sql.DB, genID string) (timingRow, bool) {
	t.Helper()
	var r timingRow
	err := conn.QueryRow(`
		SELECT chat_id, tenant_id, post_ms, ws_connect_ms, first_event_ms, first_progress_ms, completed_ms, preview_visible_ms
		FROM generation_client_timing WHERE generation_id = ?
	`, genID).Scan(&r.chatID, &r.tenantID, &r.post, &r.wsConnect, &r.firstEvent, &r.firstProgress, &r.completed, &r.previewSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return r, false
	}
	if err != nil {
		t.Fatalf("read generation_client_timing: %v", err)
	}
	return r, true
}

func ms(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

func TestClientTiming_HappyPath(t *testing.T) {
	f := newTimingFixture(t)
	rec := f.post(t, NewClientTimingHandler(f.buildSvc), f.tenantID, f.chatID, f.genID,
		`{"post_ms":180,"ws_connect_ms":240,"first_event_ms":900,"first_progress_ms":2100,"completed_ms":41000,"preview_visible_ms":42500,"browser":"ignored"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	got, ok := readTiming(t, f.conn, f.genID)
	if !ok {
		t.Fatal("no row written")
	}
	want := timingRow{chatID: f.chatID, tenantID: f.tenantID, post: ms(180), wsConnect: ms(240), firstEvent: ms(900),
		firstProgress: ms(2100), completed: ms(41000), previewSeen: ms(42500)}
	if got != want {
		t.Fatalf("row = %+v, want %+v", got, want)
	}
}

func TestClientTiming_NotFound(t *testing.T) {
	a := newTimingFixture(t)
	other := newTimingFixture(t)
	tests := []struct {
		name     string
		tenantID uint64
		chatID   string
		genID    string
	}{
		{"another tenant using real ids", a.tenantID + 1, a.chatID, a.genID},
		{"a generation from a different chat", a.tenantID, a.chatID, other.genID},
		{"an unknown generation", a.tenantID, a.chatID, uuid.NewString()},
		{"an unknown chat", a.tenantID, uuid.NewString(), a.genID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := a.post(t, NewClientTimingHandler(a.buildSvc), tt.tenantID, tt.chatID, tt.genID, `{"post_ms":100}`)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
			}
			if _, ok := readTiming(t, a.conn, tt.genID); ok {
				t.Fatal("a rejected report wrote a row")
			}
		})
	}
}

func TestClientTiming_RejectsOutOfRangeValues(t *testing.T) {
	// Like every handler here: a value out of range is a 422 naming the field; malformed JSON is a 400.
	tests := []struct {
		name string
		body string
		want int
	}{
		{"over 10 minutes", `{"completed_ms":600001}`, http.StatusUnprocessableEntity},
		{"over 10 minutes alongside valid fields", `{"post_ms":100,"preview_visible_ms":900000}`, http.StatusUnprocessableEntity},
		{"negative", `{"post_ms":-5}`, http.StatusBadRequest},
		{"not a number", `{"post_ms":"fast"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTimingFixture(t)
			rec := f.post(t, NewClientTimingHandler(f.buildSvc), f.tenantID, f.chatID, f.genID, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.want, rec.Body.String())
			}
			if _, ok := readTiming(t, f.conn, f.genID); ok {
				t.Fatal("a rejected report wrote a row")
			}
		})
	}
	f := newTimingFixture(t)
	if rec := f.post(t, NewClientTimingHandler(f.buildSvc), f.tenantID, f.chatID, f.genID, `{"completed_ms":600000}`); rec.Code != http.StatusNoContent {
		t.Fatalf("exactly 10 minutes should be accepted, got %d", rec.Code)
	}
}

func TestClientTiming_RepeatPostUpserts(t *testing.T) {
	f := newTimingFixture(t)
	h := NewClientTimingHandler(f.buildSvc)
	steps := []struct {
		body string
		want timingRow
	}{
		{`{"post_ms":180,"ws_connect_ms":240}`,
			timingRow{post: ms(180), wsConnect: ms(240)}},
		// A retry of the same report changes nothing.
		{`{"post_ms":180,"ws_connect_ms":240}`,
			timingRow{post: ms(180), wsConnect: ms(240)}},
		// A later report fills in what's new, keeps what it leaves out, and corrects what it repeats.
		{`{"ws_connect_ms":260,"completed_ms":41000,"preview_visible_ms":42500}`,
			timingRow{post: ms(180), wsConnect: ms(260), completed: ms(41000), previewSeen: ms(42500)}},
	}
	for i, s := range steps {
		if rec := f.post(t, h, f.tenantID, f.chatID, f.genID, s.body); rec.Code != http.StatusNoContent {
			t.Fatalf("post %d: status = %d: %s", i+1, rec.Code, rec.Body.String())
		}
		got, ok := readTiming(t, f.conn, f.genID)
		if !ok {
			t.Fatalf("post %d: no row", i+1)
		}
		want := s.want
		want.chatID, want.tenantID = f.chatID, f.tenantID
		if got != want {
			t.Fatalf("after post %d row = %+v, want %+v", i+1, got, want)
		}
	}
	var rows int
	if err := f.conn.QueryRow(`SELECT COUNT(*) FROM generation_client_timing WHERE generation_id = ?`, f.genID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("rows for the generation = %d (err %v), want exactly 1", rows, err)
	}
}

// Timing reports have a budget of their own, so a chatty browser can never cost the tenant a generation send.
func TestClientTiming_RateLimitedSeparately(t *testing.T) {
	f := newTimingFixture(t)
	h := NewClientTimingHandler(f.buildSvc)
	for i := 0; i < clientTimingRatePerMin; i++ {
		if rec := f.post(t, h, f.tenantID, f.chatID, f.genID, `{"post_ms":1}`); rec.Code != http.StatusNoContent {
			t.Fatalf("report %d within the budget got %d", i+1, rec.Code)
		}
	}
	if rec := f.post(t, h, f.tenantID, f.chatID, f.genID, `{"post_ms":1}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("report over the budget got %d, want 429", rec.Code)
	}
	if rec := f.post(t, h, f.tenantID+1, f.chatID, f.genID, `{"post_ms":1}`); rec.Code == http.StatusTooManyRequests {
		t.Fatal("another tenant shares this tenant's timing budget")
	}
}
