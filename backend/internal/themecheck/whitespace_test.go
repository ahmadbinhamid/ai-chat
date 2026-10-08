package themecheck

import "testing"

func TestWhitespaceOnlyChange(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		want          bool
	}{
		{"CRLF to LF", "<button\r\n  type=\"button\"\r\n>Add</button>\r\n", "<button\n  type=\"button\"\n>Add</button>\n", true},
		{"lone CR to LF", "a\rb\r", "a\nb\n", true},
		{"re-indented", "{% if x %}\n<p>hi</p>\n{% endif %}", "{% if x %}\n    <p>hi</p>\n{% endif %}", true},
		{"trailing whitespace", "a {  \n}\t\n", "a {\n}\n", true},
		{"blank lines", "a\n\n\nb\n", "a\nb\n", true},
		{"identical is not a change at all", "a\nb", "a\nb", false},
		{"real edit", "data-variant-id=\"{{ v }}\"", "data-variant-id=\"{{ product.default_variant_id }}\"", false},
		{"spacing inside a line is real", "content: 'a b';", "content: 'a  b';", false},
		{"added line", "a\n", "a\nb\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WhitespaceOnlyChange(tt.before, tt.after); got != tt.want {
				t.Errorf("WhitespaceOnlyChange() = %v, want %v", got, tt.want)
			}
		})
	}
}
