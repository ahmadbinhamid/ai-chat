package themebuild

import (
	"errors"
	"strings"
	"testing"

	"ai-chat/internal/ai"
)

func TestShouldEscalateSimpleEdit(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("unrelated"), false},
		{ai.ErrMaxTokensTruncated, true},
		{ai.ErrSimpleEditBudget, true},
		{errors.New("simple_edit: edit patches for x are too large (6834 chars)"), true},
		{errors.Join(ai.ErrSimpleEditBudget, errors.New("full-file update")), true},
	}
	for _, tc := range cases {
		if got := shouldEscalateSimpleEdit(tc.err); got != tc.want {
			t.Errorf("shouldEscalateSimpleEdit(%v)=%v want %v", tc.err, got, tc.want)
		}
	}
}

func TestSanitizeNoLongerAsksSmallerRequest(t *testing.T) {
	msg := ai.SanitizeError(ai.ErrSimpleEditBudget)
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "smaller") {
		t.Fatalf("merchant message must not ask for a smaller request: %q", msg)
	}
	if !strings.Contains(lower, "larger edit pass") {
		t.Fatalf("SanitizeError=%q want larger edit pass", msg)
	}
}
