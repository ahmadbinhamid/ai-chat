package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/modules/themebuild"

	"github.com/gin-gonic/gin"
)

// TestAttachmentHandler_ServesBytesForOwnAttachment attaches an image via the real write
// path and confirms the byte-serving route returns the exact bytes, media type, and ETag/304.
func TestAttachmentHandler_ServesBytesForOwnAttachment(t *testing.T) {
	conn := openStreamTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, nil, nil)

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, themebuild.ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	userID := uint64(1)
	imageBytes := "fake-png-bytes"
	msg, err := chatSvc.RecordUserMessage(ctx, c, &userID, "", "", "look at this",
		[]chat.MessageImage{{Base64: "ZmFrZS1wbmctYnl0ZXM=", MediaType: "image/png"}}, nil, nil) // base64("fake-png-bytes")
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(msg.Attachments))
	}
	attachmentID := msg.Attachments[0].ID

	router := gin.New()
	router.Use(fakeAuthMiddleware(tenantID))
	router.GET("/chats/:chatId/messages/:messageId/attachments/:attachmentId", NewAttachmentHandler(buildSvc).Get)

	path := "/chats/" + c.ID + "/messages/" + msg.ID + "/attachments/" + attachmentID
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != imageBytes {
		t.Errorf("expected raw bytes %q, got %q", imageBytes, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("expected Content-Type image/png, got %q", ct)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag header")
	}

	req2 := httptest.NewRequest(http.MethodGet, path, nil)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("expected 304 on a matching If-None-Match, got %d", rec2.Code)
	}
}

// TestAttachmentHandler_SetsPrivateCacheControl proves the response is marked private, since
// the URL carries no tenant id and an intermediary could otherwise share-cache it across tenants.
func TestAttachmentHandler_SetsPrivateCacheControl(t *testing.T) {
	conn := openStreamTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, nil, nil)

	tenantID := uint64(time.Now().UnixNano())
	ctx := context.Background()
	c, err := chatSvc.GetOrCreateChat(ctx, tenantID, themebuild.ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	userID := uint64(1)
	msg, err := chatSvc.RecordUserMessage(ctx, c, &userID, "", "", "look at this",
		[]chat.MessageImage{{Base64: "QUFBQQ==", MediaType: "image/png"}}, nil, nil)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	router := gin.New()
	router.Use(fakeAuthMiddleware(tenantID))
	router.GET("/chats/:chatId/messages/:messageId/attachments/:attachmentId", NewAttachmentHandler(buildSvc).Get)

	path := "/chats/" + c.ID + "/messages/" + msg.ID + "/attachments/" + msg.Attachments[0].ID
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "private") {
		t.Errorf("expected Cache-Control to include \"private\", got %q", cc)
	}
}

// TestAttachmentHandler_OtherTenantGets404 proves the ownership chain blocks cross-tenant
// access to an attachment id that's real and belongs to someone else.
func TestAttachmentHandler_OtherTenantGets404(t *testing.T) {
	conn := openStreamTestDB(t)
	chatRepo := chat.NewRepository(conn)
	chatSvc := chat.NewService(chatRepo)
	buildRepo := themebuild.NewRepository(conn)
	buildSvc := themebuild.NewService(buildRepo, chatSvc, nil, nil, nil)

	owner := uint64(time.Now().UnixNano())
	ctx := context.Background()
	c, err := chatSvc.GetOrCreateChat(ctx, owner, themebuild.ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	userID := uint64(1)
	msg, err := chatSvc.RecordUserMessage(ctx, c, &userID, "", "", "look at this",
		[]chat.MessageImage{{Base64: "QUFBQQ==", MediaType: "image/png"}}, nil, nil)
	if err != nil {
		t.Fatalf("RecordUserMessage failed: %v", err)
	}

	router := gin.New()
	router.Use(fakeAuthMiddleware(owner + 1))
	router.GET("/chats/:chatId/messages/:messageId/attachments/:attachmentId", NewAttachmentHandler(buildSvc).Get)

	path := "/chats/" + c.ID + "/messages/" + msg.ID + "/attachments/" + msg.Attachments[0].ID
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for another tenant's attachment, got %d: %s", rec.Code, rec.Body.String())
	}
}
