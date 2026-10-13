package pageintent

import "testing"

func TestDetectRedesign(t *testing.T) {
	tests := []struct {
		prompt       string
		hasReference bool
		want         bool
	}{
		{"redesign footer", false, true},
		{"Redesign the homepage", false, true},
		{"redesig the hero", false, true},
		{"re-design the cart page", false, true},
		{"revamp the home page", false, true},
		{"give the store a makeover", false, true},
		{"my shop needs a make-over", false, true},
		{"I want a new look for the homepage", false, true},
		{"give it a fresh look", false, true},
		{"make it a modern look", false, true},
		{"make the homepage look premium", false, true},
		{"I want a stunning design for my coffee shop", false, true},
		{"make it look more professional", false, true},
		{"rebuild the page", false, true},
		{"rebuild my whole homepage", false, true},
		{"completely change the home page", false, true},
		{"make it look like a real brand", false, true},
		{"make it look like this", true, true},
		{"make the homepage look like the attached site", true, true},
		{"style it like the reference", true, true},

		// Without a reference, "look like" is an ordinary request.
		{"make it look like this", false, false},
		{"make the button look like a link", false, false},
		// Adjustments, copy changes and fixes are not redesigns.
		{"make the button red", false, false},
		{"change the heading text", false, false},
		{"fix the cart", false, false},
		{"the add to cart button does nothing, fix it", false, false},
		{"make the hero text bigger", false, false},
		{"replace the header logo", false, false},
		{"rebuild the footer links", false, false},
		{"change the font size of the footer", false, false},
		{"still not working", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := DetectRedesign(tt.prompt, tt.hasReference); got != tt.want {
				t.Errorf("DetectRedesign(%q, %v) = %v, want %v", tt.prompt, tt.hasReference, got, tt.want)
			}
		})
	}
}
