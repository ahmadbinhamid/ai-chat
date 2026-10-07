package previewerrors

import (
	"strings"
	"testing"
)

func TestFeatureNotes(t *testing.T) {
	tests := []struct {
		text           string
		wantUntestable bool
		wantCart       bool
	}{
		// Checkout and customer accounts.
		{"the checkout button does nothing", true, false},
		{"fix the checkout", true, false},
		{"login isn't working", true, false},
		{"I can't log in on the preview", true, false},
		{"the register form fails to submit", true, false},
		{"sign up doesn't work", true, false},
		{"my account page is broken", true, false},
		{"orders page shows an error", true, false},
		// Cart and basket.
		{"the add to cart button does nothing, fix it", false, true},
		{"Add to Cart is not working", false, true},
		{"fix the cart", false, true},
		{"the basket doesn't update when I change the quantity", false, true},
		{"minicart won't open", false, true},
		// Both mentioned.
		{"after add to cart, checkout fails", true, true},
		// Styling negatives.
		{"make the checkout button bigger", false, false},
		{"restyle the cart page", false, false},
		{"change the cart icon colour", false, false},
		{"fix the alignment of the login form", false, false},
		{"move the account link to the right", false, false},
		// Unrelated functionality and plain requests.
		{"the slider doesn't work", false, false},
		{"make the header background dark", false, false},
		{"still not working", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := MentionsUntestableFeature(tt.text); got != tt.wantUntestable {
				t.Errorf("MentionsUntestableFeature(%q) = %v, want %v", tt.text, got, tt.wantUntestable)
			}
			if got := MentionsCartFeature(tt.text); got != tt.wantCart {
				t.Errorf("MentionsCartFeature(%q) = %v, want %v", tt.text, got, tt.wantCart)
			}
		})
	}
}

func TestFeatureNotes_AmbiguousWords(t *testing.T) {
	for _, text := range []string{
		"the bag product page is broken",
		"in order to fix the slider, it doesn't work on mobile",
		"the order of the homepage sections is broken",
	} {
		if MentionsUntestableFeature(text) || MentionsCartFeature(text) {
			t.Errorf("%q must get neither note", text)
		}
	}
}

func TestFeatureNotes_FollowUps(t *testing.T) {
	cart := []string{CartFeatureNote}
	checkout := []string{UntestableFeatureNote}
	tests := []struct {
		name    string
		prompt  string
		earlier []string // newest first
		want    []string
	}{
		{name: "follow-up inherits the cart note", prompt: "still not working",
			earlier: []string{"the add to cart button does nothing, fix it"}, want: cart},
		{name: "second follow-up still inherits", prompt: "Still not working",
			earlier: []string{"still not working", "the add to cart button does nothing, fix it", "make the header dark"}, want: cart},
		{name: "follow-up inherits the checkout note", prompt: "it didn't work",
			earlier: []string{"the checkout button does nothing"}, want: checkout},
		{name: "own request wins over the earlier one", prompt: "login still fails",
			earlier: []string{"fix the cart"}, want: checkout},
		{name: "follow-up after a styling request gets nothing", prompt: "still not working",
			earlier: []string{"make the header background dark", "fix the cart"}},
		{name: "a new styling request is not a follow-up", prompt: "make the cart icon bigger again",
			earlier: []string{"fix the cart"}},
		{name: "a fresh request with no follow-up cue gets nothing", prompt: "the slider doesn't work",
			earlier: []string{"fix the cart"}},
		{name: "lookback stops after three messages", prompt: "still not working",
			earlier: []string{"still not working", "still broken", "same issue", "fix the cart"}},
		{name: "no earlier messages", prompt: "still not working"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FeatureNotes(tt.prompt, tt.earlier)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("FeatureNotes(%q) = %q, want %q", tt.prompt, got, tt.want)
			}
		})
	}
}
