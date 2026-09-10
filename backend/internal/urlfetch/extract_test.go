package urlfetch

import "testing"

func TestExtractFirstURL(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"bare url", "https://example.com", "https://example.com", true},
		{"url first, prompt text after", "https://www.palmo.co.in/ can you access this link", "https://www.palmo.co.in/", true},
		{"prose first, url after", "check out https://example.com/page for reference", "https://example.com/page", true},
		{"trailing period", "look at https://example.com.", "https://example.com", true},
		{"trailing comma", "https://example.com, thanks", "https://example.com", true},
		{"trailing closing paren", "our site (https://example.com) looks like this", "https://example.com", true},
		{"no url", "just plain text, no links here", "", false},
		{"plain http also matches", "http://example.com", "http://example.com", true},
		{"first of two urls wins", "https://a.example.com and https://b.example.com", "https://a.example.com", true},
		{"empty string", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractFirstURL(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Errorf("ExtractFirstURL(%q) = %q, %v; want %q, %v", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}
