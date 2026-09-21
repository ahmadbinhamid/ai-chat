package themebuild

import "testing"

func TestIsSimpleInteractiveEdit(t *testing.T) {
	cases := []struct {
		prompt string
		mode   string
		want   bool
	}{
		{"can you change the header desgin please do it fast", "", true},
		{"change the header", "edit", true},
		{"redesign the footer", "", false},
		{"make the footer text white", "", true},
		{"hi", "", false},
		{"what does the header say?", "", false},
		{"rebuild the entire theme from scratch", "", false},
		{"change the header using https://example.com", "", false},
		{"change everything on all pages", "", false},
	}
	for _, tc := range cases {
		got := isSimpleInteractiveEdit(tc.prompt, tc.mode)
		if got != tc.want {
			t.Errorf("isSimpleInteractiveEdit(%q, %q)=%v want %v", tc.prompt, tc.mode, got, tc.want)
		}
	}
}
