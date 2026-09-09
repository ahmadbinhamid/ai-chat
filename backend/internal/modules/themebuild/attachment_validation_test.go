package themebuild

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
)

// Every failure case here returns before Generate ever touches s.chats/
// s.repo (see Generate's own validation order), so a bare &Service{gen: fg}
// — no real database — is enough; only the "valid input is accepted" case
// at the bottom needs newQueueTestService's real DB, since success proceeds
// into GetOrCreateChat/RecordUserMessage.

func TestGenerate_TooManyImages(t *testing.T) {
	svc := &Service{gen: &fakeGenerator{visionSupported: true}}
	images := make([]chat.MessageImage, maxImagesPerMessage+1)
	for i := range images {
		images[i] = chat.MessageImage{Base64: "AAAA", MediaType: "image/png"}
	}

	_, err := svc.Generate(context.Background(), GenerateInput{ThemeSlug: "demo", Prompt: "hi", Images: images})
	if !errors.Is(err, ErrTooManyImages) {
		t.Fatalf("expected ErrTooManyImages, got %v", err)
	}
}

// TestGenerate_ImagesAtCapAreNotRejectedForCount needs a real database:
// exactly maxImagesPerMessage images pass every validation check (count,
// vision-support, per-image size), so Generate proceeds past validation
// into GetOrCreateChat/RecordUserMessage same as the success case below.
func TestGenerate_ImagesAtCapAreNotRejectedForCount(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{visionSupported: true, results: []*ai.Result{{Summary: "ok"}}}
	images := make([]chat.MessageImage, maxImagesPerMessage)
	for i := range images {
		images[i] = chat.MessageImage{Base64: "AAAA", MediaType: "image/png"}
	}

	tenantID := uint64(time.Now().UnixNano())
	_, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "hi", Images: images,
	})
	if errors.Is(err, ErrTooManyImages) {
		t.Fatalf("expected exactly maxImagesPerMessage images to pass the count check, got %v", err)
	}
	if err != nil {
		t.Fatalf("expected exactly maxImagesPerMessage images to be accepted outright, got %v", err)
	}
}

func TestGenerate_ImageAttachedButVisionNotConfigured(t *testing.T) {
	svc := &Service{gen: &fakeGenerator{visionSupported: false}}

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "hi",
		Images: []chat.MessageImage{{Base64: "AAAA", MediaType: "image/png"}},
	})
	if !errors.Is(err, ErrVisionNotConfigured) {
		t.Fatalf("expected ErrVisionNotConfigured, got %v", err)
	}
}

func TestGenerate_ImageTooLarge(t *testing.T) {
	svc := &Service{gen: &fakeGenerator{visionSupported: true}}
	// The size check is base64.StdEncoding.DecodedLen(len(base64)) — pure
	// arithmetic on the string's own length, never an actual decode — so
	// this doesn't need to be real base64 content, just the right length.
	oversized := strings.Repeat("A", (MaxImageAttachmentBytes/3+1)*4)

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "hi",
		Images: []chat.MessageImage{{Base64: oversized, MediaType: "image/png"}},
	})
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge, got %v", err)
	}
}

// TestGenerate_ImageUnderCapIsNotRejectedForSize needs a real database: an
// under-cap image passes every validation check, so Generate proceeds past
// validation into GetOrCreateChat/RecordUserMessage same as the success
// case below.
func TestGenerate_ImageUnderCapIsNotRejectedForSize(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{visionSupported: true, results: []*ai.Result{{Summary: "ok"}}}
	underCap := strings.Repeat("A", (MaxImageAttachmentBytes/3-1)*4)

	tenantID := uint64(time.Now().UnixNano())
	_, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "hi",
		Images: []chat.MessageImage{{Base64: underCap, MediaType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("expected an under-cap image to pass the size check and be accepted, got %v", err)
	}
}

func TestGenerate_HTMLAttachmentRawUploadTooLarge(t *testing.T) {
	svc := &Service{}
	filename := "page.html"
	oversized := strings.Repeat("a", MaxHTMLUploadBytes+1)

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "hi",
		HTMLAttachmentFilename: &filename,
		HTMLAttachmentContent:  &oversized,
	})
	if !errors.Is(err, ErrHTMLAttachmentTooLarge) {
		t.Fatalf("expected ErrHTMLAttachmentTooLarge for an oversized raw upload, got %v", err)
	}
}

// TestGenerate_HTMLAttachmentStillTooLargeAfterStripping covers the case
// the raw-upload check alone can't catch: content under MaxHTMLUploadBytes
// (5MB) but with nothing for SanitizeHTMLAttachment to strip (no scripts,
// no embedded base64 assets), so it's still over MaxHTMLAttachmentBytes
// (300KB) after stripping — the post-strip check must catch it separately.
func TestGenerate_HTMLAttachmentStillTooLargeAfterStripping(t *testing.T) {
	svc := &Service{}
	filename := "page.html"
	// Plain text only — SanitizeHTMLAttachment removes nothing from this,
	// so its length is unchanged by stripping.
	content := strings.Repeat("<p>real paragraph text, nothing to strip</p>", (MaxHTMLAttachmentBytes/44)+100)
	if len(content) <= MaxHTMLUploadBytes && len(content) <= MaxHTMLAttachmentBytes {
		t.Fatalf("test setup bug: fixture content (%d bytes) doesn't actually exceed MaxHTMLAttachmentBytes (%d)", len(content), MaxHTMLAttachmentBytes)
	}

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "hi",
		HTMLAttachmentFilename: &filename,
		HTMLAttachmentContent:  &content,
	})
	if !errors.Is(err, ErrHTMLAttachmentTooLarge) {
		t.Fatalf("expected ErrHTMLAttachmentTooLarge for content still too large post-strip, got %v", err)
	}
}

// TestGenerate_HTMLAttachmentUnderCapIsSanitizedNotRejected needs a real
// database: a small HTML attachment passes both size checks, so Generate
// proceeds past validation into GetOrCreateChat/RecordUserMessage same as
// the success case below.
func TestGenerate_HTMLAttachmentUnderCapIsSanitizedNotRejected(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{results: []*ai.Result{{Summary: "ok"}}}
	filename := "page.html"
	content := `<script>alert('hi')</script><h1>Real Title</h1>`

	tenantID := uint64(time.Now().UnixNano())
	_, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "hi",
		HTMLAttachmentFilename: &filename,
		HTMLAttachmentContent:  &content,
	})
	if err != nil {
		t.Fatalf("expected a small HTML attachment to pass the size checks and be accepted, got %v", err)
	}
}

// TestGenerate_ValidImageAttachmentIsAccepted is the one case here that
// needs a real database — success proceeds past validation into
// GetOrCreateChat/RecordUserMessage, unlike every failure case above.
func TestGenerate_ValidImageAttachmentIsAccepted(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{visionSupported: true, results: []*ai.Result{{Summary: "ok"}}}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "redesign the homepage",
		Images: []chat.MessageImage{{Base64: "AAAA", MediaType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("expected a valid image attachment to be accepted, got error: %v", err)
	}
	if outcome.UserMessage.ID == "" {
		t.Fatal("expected a recorded user message")
	}
}
