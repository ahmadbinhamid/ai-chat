package builderoperations

import (
	"context"
	"strings"
	"testing"

	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

type memStore struct {
	files map[string]string
}

func (m *memStore) ReadFile(_ context.Context, _ themefs.RequestAuth, relPath string) (string, error) {
	return m.files[relPath], nil
}
func (m *memStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return nil
}
func (m *memStore) DeleteFile(context.Context, themefs.RequestAuth, string) error { return nil }
func (m *memStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	out := make([]themefs.FileTreeEntry, 0, len(m.files))
	for p := range m.files {
		out = append(out, themefs.FileTreeEntry{Path: p, Name: p, Type: "file"})
	}
	return out, nil
}

func TestMatchesRegisterExistingPrompt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prompt string
		want   bool
	}{
		{"can you register the blog page", true},
		{"if the blog page is not registered, register it", true},
		{"if not register then please register it", true},
		{"please register the blog page", true},
		{"create a contact page", false},
		{"delete extra pages", false},
		{"change button color", false},
	}
	for _, tc := range cases {
		if got := MatchesRegisterExistingPrompt(tc.prompt); got != tc.want {
			t.Fatalf("%q: got %v want %v", tc.prompt, got, tc.want)
		}
	}
}

func TestRegisterExistingPage_SuccessPreservesOthers(t *testing.T) {
	t.Parallel()
	store := &memStore{files: map[string]string{
		"pages.json": `[
  {"slug":"home","page":"home","type":"home","status":"published"},
  {"slug":"about-us","page":"about-us","type":"custom","status":"published"}
]`,
		"pages/home.liquid":     "home",
		"pages/about-us.liquid": "about",
		"pages/blog.liquid":     "{% layout %}blog body",
	}}
	res, err := RegisterExistingPage{}.Execute(context.Background(), Input{
		Prompt: "can you register the blog page",
		Store:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeSuccess {
		t.Fatalf("outcome=%s msg=%q", res.Outcome, res.UserMessage)
	}
	if res.Metrics.DeepSeekCalls != 0 || !res.Metrics.Success {
		t.Fatalf("metrics=%+v", res.Metrics)
	}
	if !strings.Contains(res.UserMessage, "registered successfully") {
		t.Fatalf("msg=%q", res.UserMessage)
	}
	var merged string
	for _, f := range res.Files {
		if f.Path == "pages.json" {
			merged = f.Content
		}
		if f.Path == "pages/blog.liquid" && f.Content != "{% layout %}blog body" {
			t.Fatal("must not regenerate page content")
		}
	}
	if !strings.Contains(merged, `"home"`) || !strings.Contains(merged, `"about-us"`) || !strings.Contains(merged, `"blog"`) {
		t.Fatalf("merged lost entries: %s", merged)
	}
}

func TestRegisterExistingPage_AlreadyRegistered(t *testing.T) {
	t.Parallel()
	store := &memStore{files: map[string]string{
		"pages.json":        `[{"slug":"blog","page":"blog","type":"blog","status":"published"}]`,
		"pages/blog.liquid": "blog",
	}}
	res, err := RegisterExistingPage{}.Execute(context.Background(), Input{
		Prompt: "can you register the blog page",
		Store:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeAlreadyDone || res.HasChanges() {
		t.Fatalf("outcome=%s files=%d", res.Outcome, len(res.Files))
	}
	if !strings.Contains(res.UserMessage, "already registered") {
		t.Fatalf("msg=%q", res.UserMessage)
	}
	if !res.Metrics.AlreadyDone {
		t.Fatalf("metrics=%+v", res.Metrics)
	}
}

func TestRegisterExistingPage_NotFound(t *testing.T) {
	t.Parallel()
	store := &memStore{files: map[string]string{
		"pages.json":        `[{"slug":"home","page":"home"}]`,
		"pages/home.liquid": "home",
	}}
	res, err := RegisterExistingPage{}.Execute(context.Background(), Input{
		Prompt: "please register the blog page",
		Store:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != OutcomeNotFound || res.HasChanges() {
		t.Fatalf("outcome=%s", res.Outcome)
	}
	if res.UserMessage != msgNotFound {
		t.Fatalf("msg=%q", res.UserMessage)
	}
}

func TestResolve_FromPlan(t *testing.T) {
	t.Parallel()
	plan := builderplan.BuilderPlan{
		Intent: builderplan.IntentNavigationRegistry,
		Operations: []builderplan.Operation{{
			Kind: builderplan.OpRegisterExistingPage, Target: "pages/blog.liquid", ProtectExisting: true,
		}},
	}
	name, ok := Resolve("unrelated", &plan)
	if !ok || name != NameRegisterExistingPage {
		t.Fatalf("got %q ok=%v", name, ok)
	}
}

func TestNeedsDeepSeekBypassViaIsDeterministicPlan(t *testing.T) {
	t.Parallel()
	plan := builderplan.BuilderPlan{
		Intent: builderplan.IntentNavigationRegistry,
		Operations: []builderplan.Operation{{
			Kind: builderplan.OpRegisterExistingPage, ProtectExisting: true,
		}},
	}
	if !IsDeterministicPlan(plan) {
		t.Fatal("expected deterministic")
	}
}
