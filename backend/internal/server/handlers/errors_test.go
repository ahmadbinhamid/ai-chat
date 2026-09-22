package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-chat/internal/modules/chat"

	"github.com/gin-gonic/gin"
)

func recordRespondErr(t *testing.T, err error) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	respondErr(c, err)

	var body map[string]any
	if w.Body.Len() > 0 {
		if jsonErr := json.Unmarshal(w.Body.Bytes(), &body); jsonErr != nil {
			t.Fatalf("failed to decode response body: %v", jsonErr)
		}
	}
	return w, body
}

// A raw themefs read failure (521) must map to an actionable message, not the generic 500 indistinguishable from a real bug.
func TestRespondErr_UpstreamUnavailableGetsActionableMessage(t *testing.T) {
	err := fmt.Errorf("read components/css/reviews.css: unexpected status 521: {\"title\":\"Error 521: Web server is down\"}")
	w, body := recordRespondErr(t, err)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
	if body["code"] != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("expected code UPSTREAM_UNAVAILABLE, got %+v", body)
	}
	msg, _ := body["error"].(string)
	if msg == "" || msg == "an unexpected error occurred" {
		t.Fatalf("expected an actionable message, got %q", msg)
	}
	// Never leak the raw Cloudflare/FlowPOS response body to the merchant.
	if want := "Web server is down"; strings.Contains(msg, want) {
		t.Fatalf("expected the raw upstream error body not to leak into the response, got %q", msg)
	}
}

func TestRespondErr_UpstreamUnavailable_502And503(t *testing.T) {
	for _, code := range []string{"502", "503"} {
		t.Run(code, func(t *testing.T) {
			err := fmt.Errorf("read pages/home.liquid: unexpected status %s: Bad Gateway", code)
			w, body := recordRespondErr(t, err)
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("expected 503, got %d", w.Code)
			}
			if body["code"] != "UPSTREAM_UNAVAILABLE" {
				t.Fatalf("expected code UPSTREAM_UNAVAILABLE, got %+v", body)
			}
		})
	}
}

// A genuinely unrecognized error must still fall back to the generic, non-leaky message.
func TestRespondErr_UnrecognizedErrorStaysGeneric(t *testing.T) {
	err := errors.New("dial tcp 10.0.0.5:3306: connect: connection refused")
	w, body := recordRespondErr(t, err)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
	if body["error"] != "an unexpected error occurred" {
		t.Fatalf("expected the generic message, got %+v", body)
	}
	if _, hasCode := body["code"]; hasCode {
		t.Fatalf("expected no code on the generic branch, got %+v", body)
	}
}

// Sentinel-error mappings (chat.ErrNotFound etc.) must still take priority over the upstream-unavailable check.
func TestRespondErr_SentinelErrorsStillMapCorrectly(t *testing.T) {
	w, body := recordRespondErr(t, chat.ErrNotFound)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	if body["code"] != "NOT_FOUND" {
		t.Fatalf("expected code NOT_FOUND, got %+v", body)
	}
}
