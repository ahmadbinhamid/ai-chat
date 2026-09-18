package themebuild

import (
	"testing"

	"ai-chat/internal/ai"
)

func TestIncompleteMultiPageCreateProposal_RejectsBlogIndexOnly(t *testing.T) {
	prompt := "please genrate 10 blogs pages for software house company page related"
	err := incompleteMultiPageCreateProposal(prompt, &ai.Result{
		Summary: "Generated 10 blog pages for your software house.",
		Files: []ai.GeneratedFile{
			{Path: "pages/blog.liquid", Action: "update", Content: "x"},
			{Path: "components/card-essentials.liquid", Action: "update", Content: "y"},
		},
	})
	if err == nil {
		t.Fatal("expected rejection of blog-index-only stub")
	}
}

func TestIncompleteMultiPageCreateProposal_AcceptsFullCreate(t *testing.T) {
	prompt := "generate 3 blog pages"
	files := []ai.GeneratedFile{
		{Path: "pages.json", Action: "update", Content: "[]"},
	}
	for _, slug := range []string{"a", "b", "c"} {
		files = append(files, ai.GeneratedFile{
			Path:    "pages/" + slug + ".liquid",
			Action:  "create",
			Content: "body",
		})
	}
	if err := incompleteMultiPageCreateProposal(prompt, &ai.Result{Files: files}); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestMultiPageCreateBatchSize(t *testing.T) {
	if got := multiPageCreateBatchSize("generate 10 blog pages"); got != 3 {
		t.Fatalf("want batch 3 for 10-page ask, got %d", got)
	}
	if got := multiPageCreateBatchSize("generate 2 blog pages"); got != 2 {
		t.Fatalf("want 2 got %d", got)
	}
}
