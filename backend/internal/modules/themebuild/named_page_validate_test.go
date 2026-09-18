package themebuild

import (
	"testing"

	"ai-chat/internal/ai"
)

func TestIncompleteNamedPageRewriteProposal(t *testing.T) {
	prompt := "shop k page ko software compnay theme k mutibq regenrate kro please"
	err := incompleteNamedPageRewriteProposal(prompt, &ai.Result{Files: []ai.GeneratedFile{
		{Path: "components/contact-inquiry.liquid", Action: "update", Content: "x"},
		{Path: "components/card-essentials.liquid", Action: "update", Content: "y"},
	}})
	if err == nil {
		t.Fatal("expected rejection when shop rewrite misses products.liquid")
	}
	if err := incompleteNamedPageRewriteProposal(prompt, &ai.Result{Files: []ai.GeneratedFile{
		{Path: "pages/products.liquid", Action: "update", Content: "full"},
	}}); err != nil {
		t.Fatal(err)
	}
}
