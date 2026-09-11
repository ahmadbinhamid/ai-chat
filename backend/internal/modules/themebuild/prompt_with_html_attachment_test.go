package themebuild

import (
	"strings"
	"testing"
)

func TestPromptWithHTMLAttachment(t *testing.T) {
	filename := "reference.html"
	content := "<h1>Hello</h1>"

	t.Run("no attachment returns prompt unchanged", func(t *testing.T) {
		got := promptWithHTMLAttachment("hi", GenerateInput{})
		if got != "hi" {
			t.Errorf("expected prompt unchanged, got %q", got)
		}
	})

	t.Run("uploaded file gets the generic untrusted-content framing, not the external-link framing", func(t *testing.T) {
		got := promptWithHTMLAttachment("read this", GenerateInput{
			HTMLAttachmentFilename: &filename, HTMLAttachmentContent: &content,
		})
		if !strings.Contains(got, "UNTRUSTED content the merchant attached") {
			t.Errorf("expected the generic untrusted-content framing, got: %s", got)
		}
		if strings.Contains(got, "fetched this page's live content on your behalf") {
			t.Errorf("expected no external-link framing for an uploaded file, got: %s", got)
		}
	})

	t.Run("link-fetched content gets the external-site call-out", func(t *testing.T) {
		url := "https://example.com"
		got := promptWithHTMLAttachment("can you access this link?", GenerateInput{
			HTMLAttachmentFilename: &url, HTMLAttachmentContent: &content, HTMLAttachmentIsExternalLink: true,
		})
		for _, want := range []string{
			"UNTRUSTED content the merchant attached",
			"fetched this page's live content on your behalf",
			"you DID access it",
			"never say you can't read URLs or open external links",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("expected output to contain %q, got: %s", want, got)
			}
		}
	})

	t.Run("carried-forward upload gets the earlier-turn note on top of the generic framing", func(t *testing.T) {
		got := promptWithHTMLAttachment("build it like that", GenerateInput{
			HTMLAttachmentFilename: &filename, HTMLAttachmentContent: &content, HTMLAttachmentCarriedForward: true,
		})
		for _, want := range []string{
			"EARLIER message in this conversation",
			"still the active reference",
			"UNTRUSTED content the merchant attached",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("expected output to contain %q, got: %s", want, got)
			}
		}
		if strings.Contains(got, "fetched this page's live content on your behalf") {
			t.Errorf("expected no external-link framing for a carried-forward upload, got: %s", got)
		}
	})

	t.Run("carried-forward link gets both the earlier-turn note and the external-link framing", func(t *testing.T) {
		url := "https://example.com"
		got := promptWithHTMLAttachment("build it like that", GenerateInput{
			HTMLAttachmentFilename: &url, HTMLAttachmentContent: &content,
			HTMLAttachmentIsExternalLink: true, HTMLAttachmentCarriedForward: true,
		})
		for _, want := range []string{
			"EARLIER message in this conversation",
			"still the active reference",
			"fetched this page's live content on your behalf",
			"you DID access it",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("expected output to contain %q, got: %s", want, got)
			}
		}
	})
}
