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

func TestExtractReferenceURL(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
		ok   bool
	}{
		{"bare url dominates the prompt", "https://example.com", "https://example.com", true},
		{"bare url with only trailing whitespace", "https://example.com   ", "https://example.com", true},
		{"url plus a short question still dominates", "https://example.com ?", "https://example.com", true},
		{"cue phrase: this link", "https://example.com can you access this link", "https://example.com", true},
		{"cue phrase: this site", "https://example.com is this site accessible to you", "https://example.com", true},
		{"cue phrase: this page", "https://example.com can you read this page for me", "https://example.com", true},
		{"cue phrase: like this", "make our homepage look like this: https://example.com", "https://example.com", true},
		{"cue phrase: reference", "https://example.com use this as a design reference", "https://example.com", true},
		{"cue phrase: similar to", "we want something similar to https://example.com", "https://example.com", true},
		{"cue phrase: check", "can you check https://example.com", "https://example.com", true},
		{
			"word-boundary: checkout does not match the check cue",
			"make the checkout button blue, our site is https://example.com", "", false,
		},
		{
			"word-boundary: checkbox does not match the check cue",
			"add a checkbox to the form, see https://example.com for the field names", "", false,
		},
		{
			"word-boundary: checked does not match the check cue",
			"the checked state should be blue, our site is https://example.com", "", false,
		},
		{"cue phrase: look at", "look at https://example.com and tell me what you think", "https://example.com", true},
		{"cue phrase: clone", "clone the layout from https://example.com", "https://example.com", true},
		{"cue phrase: inspired by", "our new design should be inspired by https://example.com", "https://example.com", true},
		{"cue phrase is case-insensitive", "https://example.com CHECK this out", "https://example.com", true},
		{
			"url mentioned in passing, no cue, does not dominate",
			"our shop's own domain is https://example.com by the way, now please make the header background blue",
			"", false,
		},
		{"no url at all", "just plain text, no links here", "", false},
		{"empty string", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ExtractReferenceURL(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Errorf("ExtractReferenceURL(%q) = %q, %v; want %q, %v", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}
