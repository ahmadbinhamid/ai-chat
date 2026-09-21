package providers_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai-chat/internal/builderintelligence/providers"
)

func TestHeuristic_JPROAndVague(t *testing.T) {
	t.Parallel()
	h := providers.Heuristic{}
	ref, err := h.Extract(context.Background(), providers.Input{Prompt: "make the site better"})
	if err != nil {
		t.Fatal(err)
	}
	if !ref.NeedsClarification {
		t.Fatal("want clarification")
	}
	ref, err = h.Extract(context.Background(), providers.Input{
		Prompt: "change blogs for a software house but keep JPRO meta titles",
	})
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Join(ref.Constraints, " ")
	if !strings.Contains(blob, "JPRO") && !strings.Contains(blob, "software") {
		t.Fatalf("constraints=%v", ref.Constraints)
	}
	found := false
	for _, f := range ref.ProtectedFields {
		if f == "meta_title" {
			found = true
		}
	}
	if !found {
		t.Fatalf("protected=%v", ref.ProtectedFields)
	}
}

func TestUnavailable(t *testing.T) {
	t.Parallel()
	_, err := (providers.Unavailable{}).Extract(context.Background(), providers.Input{})
	if !errors.Is(err, providers.ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
}
