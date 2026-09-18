package ai

import (
	"errors"
	"testing"
)

func TestRejectBloatedSimpleEditProposal_FullUpdate(t *testing.T) {
	big := make([]byte, simpleEditMaxFullUpdateRunes+10)
	for i := range big {
		big[i] = 'x'
	}
	err := rejectBloatedSimpleEditProposal(&Result{Files: []GeneratedFile{{
		Path: "components/header.liquid", Action: "update", Content: string(big),
	}}})
	if err == nil {
		t.Fatal("expected rejection of full-file update")
	}
	if !IsSimpleEditBudgetError(err) {
		t.Fatalf("want IsSimpleEditBudgetError, got %v", err)
	}
	if !errors.Is(err, ErrSimpleEditBudget) {
		t.Fatalf("want ErrSimpleEditBudget wrap, got %v", err)
	}
}


func TestRejectBloatedSimpleEditProposal_EditOK(t *testing.T) {
	err := rejectBloatedSimpleEditProposal(&Result{Files: []GeneratedFile{{
		Path: "components/header.liquid", Action: "edit", Content: "",
		Edits: []Edit{{OldString: "bg-white", NewString: "bg-black"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRejectBloatedSimpleEditProposal_EmptyEdit(t *testing.T) {
	err := rejectBloatedSimpleEditProposal(&Result{Files: []GeneratedFile{{
		Path: "x.liquid", Action: "edit", Content: "", Edits: nil,
	}}})
	if err == nil {
		t.Fatal("expected empty edits rejection")
	}
}
