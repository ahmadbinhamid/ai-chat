package themebuild

import (
	"testing"

	"ai-chat/internal/ai"
)

func TestMenuLabelFromAddPrompt(t *testing.T) {
	if got := menuLabelFromAddPrompt("add Services page in menu please"); got != "Services" {
		t.Fatalf("got %q want Services", got)
	}
}

func TestIncompleteAddToMenuProposal_RejectsNoOp(t *testing.T) {
	prompt := "add Services page in menu please"
	body := `{
  "menu": {
    "items": [
      {"id": "home", "label": "Home", "url": "/", "children": []}
    ]
  }
}`
	err := incompleteAddToMenuProposal(prompt, &ai.Result{
		Summary: "Added Services",
		Files:   []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: body}},
	})
	if err == nil {
		t.Fatal("expected reject when Services missing from menu.items")
	}
}

func TestIncompleteAddToMenuProposal_AcceptsServices(t *testing.T) {
	prompt := "add Services page in menu please"
	body := `{
  "menu": {
    "items": [
      {"id": "home", "label": "Home", "url": "/", "children": []},
      {"id": "services", "label": "Services", "url": "/services", "children": []}
    ]
  }
}`
	if err := incompleteAddToMenuProposal(prompt, &ai.Result{
		Files: []ai.GeneratedFile{{Path: "defaults.json", Action: "update", Content: body}},
	}); err != nil {
		t.Fatal(err)
	}
}
