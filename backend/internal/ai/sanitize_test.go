package ai

import (
	"errors"
	"strings"
	"testing"
)

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantContain string
		mustNotHave []string
	}{
		{
			name: "credit balance too low",
			err: errors.New(`claude stream: POST "https://api.anthropic.com/v1/messages": 400 Bad Request ` +
				`{"type":"error","error":{"type":"invalid_request_error","message":"Your credit balance is too low ` +
				`to access the Anthropic API. Please go to Plans & Billing to upgrade or purchase credits."}}`),
			wantContain: "out of credits",
			mustNotHave: []string{"Anthropic", "anthropic", "claude", "api.anthropic.com"},
		},
		{
			name:        "deepseek rate limited",
			err:         errors.New(`deepseek stream: POST "https://api.deepseek.com/anthropic/v1/messages": 429 Too Many Requests`),
			wantContain: "too many requests",
			mustNotHave: []string{"DeepSeek", "deepseek", "api.deepseek.com"},
		},
		{
			name:        "max tokens truncation",
			err:         errMaxTokensTruncated,
			wantContain: "too large",
		},
		{
			name:        "context deadline",
			err:         errors.New("load theme context: context deadline exceeded"),
			wantContain: "timed out",
		},
		{
			name:        "tool-loop iterations exhausted",
			err:         errors.New("model did not call propose_changes within 28 tool-loop iterations"),
			wantContain: "too complex",
		},
		{
			name:        "themecheck validation exhausted after repairs",
			err:         errors.New("the generated changes didn't pass validation after 3 attempts: some_rule (2 files)"),
			wantContain: "couldn't be validated",
		},
		{
			name:        "unrecognized error falls back to generic",
			err:         errors.New("some completely novel failure mode nobody anticipated"),
			wantContain: "something went wrong",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeError(tt.err)
			if !strings.HasPrefix(got, "Error from AI agent: ") {
				t.Errorf("SanitizeError(%q) = %q, want prefix %q", tt.err, got, "Error from AI agent: ")
			}
			if !strings.Contains(strings.ToLower(got), tt.wantContain) {
				t.Errorf("SanitizeError(%q) = %q, want it to contain %q", tt.err, got, tt.wantContain)
			}
			for _, forbidden := range tt.mustNotHave {
				if strings.Contains(got, forbidden) {
					t.Errorf("SanitizeError(%q) = %q, must not contain %q", tt.err, got, forbidden)
				}
			}
		})
	}
}

func TestSanitizeError_Nil(t *testing.T) {
	if got := SanitizeError(nil); got != "" {
		t.Errorf("SanitizeError(nil) = %q, want empty string", got)
	}
}
