package pageintent

import "testing"

func TestDetectReplace(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		// Every listed word.
		{"redesign hero section", true},
		{"redesig hero section", true}, // the live message that was wrongly blocked, typo included
		{"rebuild the footer", true},
		{"replace the header with a centered logo", true},
		{"start over on the product page", true},
		{"make the about page from scratch", true},
		{"change the header completely", true},
		// Variants.
		{"Redesign the cart page", true},
		{"re-design the menu", true},
		{"can you rebuild it", true},
		{"replacing the banner image", true},
		{"let's start it over", true},
		// Adjustments, fixes and undo requests are not replacements.
		{"make the header background dark", false},
		{"the add to cart button does nothing, fix it", false},
		{"still not working", false},
		{"make the hero text bigger", false},
		{"undo the header change", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := DetectReplace(tt.prompt); got != tt.want {
				t.Errorf("DetectReplace(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}
