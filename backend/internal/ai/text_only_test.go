package ai

import "testing"

func TestTextOnlyNudge(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"empty reply", "", genericTextOnlyNudge},
		{"plain chatter", "Sure, I can help with that. Let me take a look.", genericTextOnlyNudge},
		{"code fence", "Here's the update:\n```css\nheader { color: red; }\n```", textEditNudge},
		{"component path", "I'll change components/header.liquid so the logo is bigger.", textEditNudge},
		{"css file path", "Update `pages/css/offers.css` with the new colours.", textEditNudge},
		{"pages.json named", "Add the route to pages.json.", textEditNudge},
		{"path inside a url is not a theme file", "See https://example.com/js/app.js for details.", genericTextOnlyNudge},
		{"directory word without a file", "The css folder holds the styles.", genericTextOnlyNudge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := textOnlyNudge(tt.text); got != tt.want {
				t.Fatalf("textOnlyNudge(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}
