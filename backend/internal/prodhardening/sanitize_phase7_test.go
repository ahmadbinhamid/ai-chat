package prodhardening_test

import (
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

func TestSanitizeError_NoSecrets(t *testing.T) {
	raw := errorsNew("anthropic: POST https://api.deepseek.com/anthropic/v1/messages: 401 api_key=sk-secret-abc Authorization: Bearer tok-xyz")
	got := ai.SanitizeError(raw)
	for _, forbidden := range []string{"sk-secret", "tok-xyz", "api_key=", "Bearer ", "deepseek.com"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("SanitizeError leaked %q via %q", forbidden, got)
		}
	}
}

func errorsNew(s string) error { return &plainErr{s} }

type plainErr struct{ s string }

func (e *plainErr) Error() string { return e.s }
