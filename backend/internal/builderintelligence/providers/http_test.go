package providers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-chat/internal/builderintelligence/providers"
)

func TestHTTP_MockSemantic(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Reject json_schema to exercise plain fallback sometimes — always succeed.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"constraints\":[\"preserve existing JPRO meta titles\"],\"protected_fields\":[\"meta_title\"],\"preferences\":[],\"clarification\":\"\",\"needs_clarification\":false,\"confidence\":0.9}"}}]}`))
	}))
	defer srv.Close()

	p := providers.HTTP{BaseURL: srv.URL + "/v1", Model: "test"}
	ref, err := p.Extract(context.Background(), providers.Input{
		Prompt: "change blogs keep JPRO meta titles",
		Plan:   providers.MiniPlanHint{Intent: "compound", Operations: []string{"update_page_content"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.ProtectedFields) == 0 || ref.ProtectedFields[0] != "meta_title" {
		t.Fatalf("%+v", ref)
	}
}

func TestHTTP_EmptyURL(t *testing.T) {
	t.Parallel()
	_, err := (providers.HTTP{}).Extract(context.Background(), providers.Input{Prompt: "x"})
	if err == nil {
		t.Fatal("expected unavailable")
	}
}
