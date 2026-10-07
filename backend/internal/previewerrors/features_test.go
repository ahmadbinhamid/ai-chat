package previewerrors

import "testing"

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
