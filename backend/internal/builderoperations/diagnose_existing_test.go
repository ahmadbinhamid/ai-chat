package builderoperations_test

import (
	"context"
	"strings"
	"testing"

	"ai-chat/internal/builderoperations"
	"ai-chat/internal/builderplan"
	"ai-chat/internal/themefs"
)

type diagMemStore struct {
	files map[string]string
}

func (m *diagMemStore) ListFiles(context.Context, themefs.RequestAuth) ([]themefs.FileTreeEntry, error) {
	var out []themefs.FileTreeEntry
	for p := range m.files {
		out = append(out, themefs.FileTreeEntry{Path: p, Name: p, Type: "file"})
	}
	return out, nil
}
func (m *diagMemStore) ReadFile(_ context.Context, _ themefs.RequestAuth, path string) (string, error) {
	return m.files[path], nil
}
func (m *diagMemStore) WriteFile(context.Context, themefs.RequestAuth, string, string, *themefs.PageMeta) error {
	return nil
}
func (m *diagMemStore) DeleteFile(context.Context, themefs.RequestAuth, string) error { return nil }

func TestDiagnoseExistingPage_MissingRegistryAutoFix(t *testing.T) {
	t.Parallel()
	store := &diagMemStore{files: map[string]string{
		"pages/blog.liquid": "{% layout-start %}blog{% layout-end %}",
		"pages.json":        `[{"page":"home","slug":"home","path":"/","type":"index","status":"published"}]`,
		"defaults.json":     `{"menu":{"items":[]}}`,
	}}
	out, err := builderoperations.Run(context.Background(), builderoperations.NameDiagnoseExistingPage, builderoperations.Input{
		Prompt: "blog page is not working please check and fix it",
		Store:  store,
		Auth:   themefs.RequestAuth{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != builderoperations.OutcomeSuccess {
		t.Fatalf("outcome=%s msg=%s", out.Outcome, out.UserMessage)
	}
	if out.Diagnosis == nil || !out.Diagnosis.FixApplied {
		t.Fatalf("expected fix applied, diag=%+v", out.Diagnosis)
	}
	if !strings.Contains(strings.ToLower(out.UserMessage), "fixed") {
		t.Fatalf("user message=%q", out.UserMessage)
	}
	hasPagesJSON := false
	for _, f := range out.Files {
		if f.Path == "pages.json" {
			hasPagesJSON = true
			if !strings.Contains(f.Content, `"blog"`) {
				t.Fatalf("merged pages.json missing blog: %s", f.Content)
			}
		}
	}
	if !hasPagesJSON {
		t.Fatal("expected pages.json staging")
	}
}

func TestDiagnoseExistingPage_HealthyNoChange(t *testing.T) {
	t.Parallel()
	store := &diagMemStore{files: map[string]string{
		"pages/blog.liquid": "{% layout-start %}blog{% layout-end %}",
		"pages.json":        `[{"page":"blog","slug":"blog","path":"/pages","type":"custom","status":"published"}]`,
		"defaults.json":     `{"menu":{"items":[{"url":"/blog"}]}}`,
	}}
	out, err := builderoperations.Run(context.Background(), builderoperations.NameDiagnoseExistingPage, builderoperations.Input{
		Prompt: "why is the blog page not opening?",
		Store:  store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != builderoperations.OutcomeNoChange {
		t.Fatalf("outcome=%s msg=%s", out.Outcome, out.UserMessage)
	}
	if out.Diagnosis == nil || !out.Diagnosis.PageExists || !out.Diagnosis.Registered {
		t.Fatalf("diag=%+v", out.Diagnosis)
	}
	if len(out.Files) != 0 {
		t.Fatal("healthy page must not mutate")
	}
}

func TestResolve_TroubleshootVsRegister(t *testing.T) {
	t.Parallel()
	n, ok := builderoperations.Resolve("can you register the blog page", nil)
	if !ok || n != builderoperations.NameRegisterExistingPage {
		t.Fatalf("register got %s ok=%v", n, ok)
	}
	n, ok = builderoperations.Resolve("blog page is not working", nil)
	if !ok || n != builderoperations.NameDiagnoseExistingPage {
		t.Fatalf("troubleshoot got %s ok=%v", n, ok)
	}
	plan := builderplan.BuilderPlan{
		Intent: builderplan.IntentPageTroubleshoot,
		Operations: []builderplan.Operation{{
			Kind: builderplan.OpDiagnoseExistingPage, Target: "pages/blog.liquid", ProtectExisting: true,
		}},
	}
	if !builderoperations.IsDeterministicPlan(plan) {
		t.Fatal("diagnose plan must be deterministic")
	}
}
