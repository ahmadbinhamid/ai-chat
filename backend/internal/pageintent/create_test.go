package pageintent

import "testing"

func TestDetectCreatePage(t *testing.T) {
	tests := []struct {
		prompt string
		want   bool
	}{
		{"create a new page called about", true},
		{"Create an about us page", true},
		{"add a FAQ page", true},
		{"build me a landing page for our coffee subscription", true},
		{"make a contact page with a form", true},
		{"set up a new careers page", true},
		{"make the page dark", false},
		{"add a section to the home page", false},
		{"add a testimonials section to the home page", false},
		{"change the about page heading", false},
		{"the contact page is broken", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.prompt, func(t *testing.T) {
			if got := DetectCreatePage(tt.prompt); got != tt.want {
				t.Errorf("DetectCreatePage(%q) = %v, want %v", tt.prompt, got, tt.want)
			}
		})
	}
}
