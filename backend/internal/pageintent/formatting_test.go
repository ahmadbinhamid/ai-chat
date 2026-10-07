package pageintent

import "testing"

func TestDetectFormatting(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"fix the indentation in header.css", true},
		{"reformat the product grid", true},
		{"format the code in footer.liquid", true},
		{"convert the line endings to LF", true},
		{"the file has CRLF line endings", true},
		{"tidy up the code", true},
		{"clean up the code in minicart.js", true},
		{"remove the trailing whitespace", true},
		{"still not working", false},
		{"the add to cart button does nothing, fix it", false},
		{"add more spacing between the cards", false},
		{"fix the padding on the header", false},
		{"change the layout of the footer", false},
		{"make the header background dark", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := DetectFormatting(tt.prompt); got != tt.want {
				t.Errorf("DetectFormatting(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}
