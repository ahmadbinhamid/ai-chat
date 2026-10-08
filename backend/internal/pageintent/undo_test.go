package pageintent

import "testing"

func TestDetectUndo(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		// Every listed cue.
		{"undo the header change", true},
		{"revert the header", true},
		{"go back to the old footer", true},
		{"restore the old colours", true},
		{"change it back", true},
		{"put it back how it was", true},
		{"remove the changes you made to the header", true},
		{"use the original header", true},
		{"bring back the previous version", true},
		// Casing, tense and close variants.
		{"UNDO that", true},
		{"can you revert what you did to the cart", true},
		{"rollback the last change", true},
		{"roll back the footer", true},
		{"change the header back", true},
		{"remove my earlier changes", true},
		{"get rid of the previous edits", true},
		{"make it look as it was before", true},
		// Functionality and styling requests that must keep the reversion check blocking.
		{"the add to cart button does nothing, fix it", false},
		{"still not working", false},
		{"make the header background dark", false},
		{"make the checkout button bigger", false},
		{"restyle the cart page", false},
		{"add a back to top button", false},
		{"the back button on the product page is broken", false},
		{"move the logo to the left", false},
		{"remove the banner from the homepage", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := DetectUndo(tt.prompt); got != tt.want {
				t.Errorf("DetectUndo(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}
