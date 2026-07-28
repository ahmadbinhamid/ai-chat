package themefs

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestMergePageRegistration_EmptyRegistry(t *testing.T) {
	out, err := MergePageRegistration(nil, PageEntry{Slug: "offers", Title: "Offers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var entries []PageEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(entries) != 1 || entries[0].Slug != "offers" {
		t.Fatalf("expected one entry with slug offers, got %+v", entries)
	}
}

func TestMergePageRegistration_AppendsToExisting(t *testing.T) {
	existing, _ := json.Marshal([]PageEntry{{Slug: "home", Title: "Home"}})

	out, err := MergePageRegistration(existing, PageEntry{Slug: "offers", Title: "Offers"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var entries []PageEntry
	_ = json.Unmarshal(out, &entries)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestMergePageRegistration_DuplicateSlugRejected(t *testing.T) {
	existing, _ := json.Marshal([]PageEntry{{Slug: "offers", Title: "Offers"}})

	_, err := MergePageRegistration(existing, PageEntry{Slug: "offers", Title: "Offers Again"})
	if !errors.Is(err, ErrSlugAlreadyRegistered) {
		t.Fatalf("expected ErrSlugAlreadyRegistered, got %v", err)
	}
}
