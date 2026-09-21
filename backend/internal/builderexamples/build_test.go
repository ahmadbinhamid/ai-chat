package builderexamples

import (
	"strings"
	"testing"
	"time"
)

func TestFingerprint_Stable(t *testing.T) {
	t.Parallel()
	a := Fingerprint("  Create 2 Blog Pages ")
	b := Fingerprint("create 2 blog pages")
	if a != b || len(a) != 64 {
		t.Fatalf("a=%s b=%s", a, b)
	}
}

func TestSanitizePrompt_RedactsSecrets(t *testing.T) {
	t.Parallel()
	s := SanitizePrompt("use key sk-abcdefghijklmnop and Bearer eyJhbGciOi and user@x.com please", 200)
	if strings.Contains(s, "sk-") || strings.Contains(s, "Bearer eyJ") || strings.Contains(s, "@") {
		t.Fatalf("not redacted: %q", s)
	}
	if !strings.Contains(s, "[REDACTED]") || !strings.Contains(s, "[EMAIL]") {
		t.Fatalf("missing markers: %q", s)
	}
}

func TestBuildExample_LocalOpPositive(t *testing.T) {
	t.Parallel()
	ex := BuildExample(Input{
		TenantID:               1,
		Prompt:                 "can you register the blog page",
		Intent:                 "navigation_registry",
		Operations:             []string{"register_existing_page"},
		DeterministicOperation: "register_existing_page",
		DeepSeekUsed:           false,
		WallStatus:             "succeeded",
		Now:                    time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
	}, false)
	if ex.PromptSanitized != "" {
		t.Fatal("default must not store sanitized prompt")
	}
	if !ex.Outcome.TrainingPositive || ex.Outcome.Category != OutcomeLocalOperationSuccess {
		t.Fatalf("outcome=%+v", ex.Outcome)
	}
	if len(ex.Execution.ToolKinds) == 0 {
		t.Fatal("expected deterministic tool trace")
	}
}

func TestBuildExample_FailureNotPositive(t *testing.T) {
	t.Parallel()
	ex := BuildExample(Input{
		TenantID:   1,
		Prompt:     "create 2 blog pages",
		Err:        errStringer("provider timeout: first token"),
		WallStatus: "failed",
	}, false)
	if ex.Outcome.TrainingPositive || !ex.Outcome.Failed {
		t.Fatalf("outcome=%+v", ex.Outcome)
	}
	if ex.Outcome.Category != OutcomeProviderTimeout {
		t.Fatalf("cat=%s", ex.Outcome.Category)
	}
}

type errStringer string

func (e errStringer) Error() string { return string(e) }

func TestBuildExample_Ambiguous(t *testing.T) {
	t.Parallel()
	ex := BuildExample(Input{
		Prompt:             "make the site better",
		Intent:             "ambiguous",
		NeedsClarification: true,
		WallStatus:         "succeeded",
	}, true)
	if ex.Outcome.Category != OutcomeAmbiguous || ex.Outcome.TrainingPositive {
		t.Fatalf("outcome=%+v", ex.Outcome)
	}
	if ex.PromptSanitized == "" {
		t.Fatal("expected sanitized when enabled")
	}
}
