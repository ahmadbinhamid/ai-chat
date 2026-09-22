package themebuild

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/urlfetch"
)

// Failure cases return before Generate touches s.chats/s.repo; only success cases need real DB.
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

// Real DB needed: exactly maxImagesPerMessage images pass all validation checks.
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
	// Size check is pure arithmetic on base64 string length (no decode needed).
	oversized := strings.Repeat("A", (MaxImageAttachmentBytes/3+1)*4)

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "hi",
		Images: []chat.MessageImage{{Base64: oversized, MediaType: "image/png"}},
	})
	if !errors.Is(err, ErrImageTooLarge) {
		t.Fatalf("expected ErrImageTooLarge, got %v", err)
	}
}

// Real DB needed: under-cap image passes all validation checks.
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

// Post-strip check catches content under 5MB but still over 300KB after stripping scripts/assets.
func TestGenerate_HTMLAttachmentStillTooLargeAfterStripping(t *testing.T) {
	svc := &Service{}
	filename := "page.html"
	// Plain text only; SanitizeHTMLAttachment removes nothing, so length unchanged.
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

// Real DB needed: small HTML attachment passes both size checks.
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

// Real DB needed: success proceeds into GetOrCreateChat/RecordUserMessage.
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

// Stands in for *urlfetch.Fetcher; no real network calls (CSS fetching is best-effort, no failure simulation).
type fakeLinkFetcher struct {
	calls     int
	lastURL   string
	content   string
	truncated bool
	err       error

	stylesheetCSS   string
	stylesheetCount int
}

func (f *fakeLinkFetcher) Fetch(_ context.Context, rawURL string, _ int64) (urlfetch.Result, error) {
	f.calls++
	f.lastURL = rawURL
	if f.err != nil {
		return urlfetch.Result{}, f.err
	}
	finalURL, _ := url.Parse(rawURL)
	return urlfetch.Result{HTML: f.content, Truncated: f.truncated, FinalURL: finalURL}, nil
}

func (f *fakeLinkFetcher) FetchStylesheets(_ context.Context, _ string, _ *url.URL) (string, int) {
	return f.stylesheetCSS, f.stylesheetCount
}

// Proves Generate never calls Fetch synchronously; blocks on unbuffered channel (test hangs if violated).
type blockingLinkFetcher struct {
	mu      sync.Mutex
	calls   int
	lastURL string
	content string
	release chan struct{}
}

func (f *blockingLinkFetcher) Fetch(_ context.Context, rawURL string, _ int64) (urlfetch.Result, error) {
	<-f.release
	f.mu.Lock()
	f.calls++
	f.lastURL = rawURL
	f.mu.Unlock()
	return urlfetch.Result{HTML: f.content}, nil
}

func (f *blockingLinkFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// FetchStylesheets never reached (Fetch blocks forever); implemented only to satisfy interface.
func (f *blockingLinkFetcher) FetchStylesheets(_ context.Context, _ string, _ *url.URL) (string, int) {
	return "", 0
}

// Phase 1 invariant: URL in prompt must not trigger synchronous network work in Generate (POST must be fast 202).
func TestGenerate_DoesNotFetchSynchronously(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{results: []*ai.Result{{Summary: "ok"}}}
	fl := &blockingLinkFetcher{content: "<h1>Reference Site</h1>", release: make(chan struct{})}
	svc.links = fl
	t.Cleanup(func() { close(fl.release) })

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "https://example.com can you access this link",
	})
	// Reaching this line proves Generate didn't call Fetch synchronously (would block forever).
	if err != nil {
		t.Fatalf("expected a link-only prompt to be accepted, got error: %v", err)
	}
	if got := fl.callCount(); got != 0 {
		t.Errorf("expected zero fetch calls from Generate itself, got %d", got)
	}

	gen, err := svc.repo.GetGenerationByID(context.Background(), outcome.Chat.ID, outcome.GenerationID)
	if err != nil {
		t.Fatalf("failed to load the enqueued generation: %v", err)
	}
	if gen.ReferenceURL != "https://example.com" {
		t.Errorf("expected the enqueued row to carry ReferenceURL %q, got %q", "https://example.com", gen.ReferenceURL)
	}
}

// One case still synchronous 4xx: URL malformed enough that ValidateURL rejects it (no network needed).
func TestGenerate_MalformedURLStillRejectedSynchronously(t *testing.T) {
	svc := &Service{gen: &fakeGenerator{}, links: &fakeLinkFetcher{err: errors.New("must never be called — no network needed for a shape rejection")}}

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "https://user:pass@example.com can you access this link",
	})
	if !errors.Is(err, ErrLinkFetchFailed) {
		t.Fatalf("expected ErrLinkFetchFailed for a malformed url, got %v", err)
	}
}

// Uploaded HTML file is more deliberate signal than URL merely mentioned; ReferenceURL stays empty.
func TestGenerate_ExplicitHTMLAttachmentWinsOverLinkInPrompt(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{results: []*ai.Result{{Summary: "ok"}}}
	filename := "page.html"
	content := "<h1>Uploaded</h1>"

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                 "https://example.com make it look like the uploaded file",
		HTMLAttachmentFilename: &filename,
		HTMLAttachmentContent:  &content,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gen, err := svc.repo.GetGenerationByID(context.Background(), outcome.Chat.ID, outcome.GenerationID)
	if err != nil {
		t.Fatalf("failed to load the enqueued generation: %v", err)
	}
	if gen.ReferenceURL != "" {
		t.Errorf("expected no ReferenceURL when an HTML file was explicitly uploaded, got %q", gen.ReferenceURL)
	}
}

func TestGenerate_NoLinkInPromptSkipsFetch(t *testing.T) {
	svc, _ := newQueueTestService(t)
	svc.gen = &fakeGenerator{results: []*ai.Result{{Summary: "ok"}}}
	fl := &fakeLinkFetcher{err: errors.New("must never be called")}
	svc.links = fl

	tenantID := uint64(time.Now().UnixNano())
	_, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "just redesign the homepage",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fl.calls != 0 {
		t.Errorf("expected the link fetcher never to be called for a prompt with no url, got %d calls", fl.calls)
	}
}

// Regression guard: Service without links set (nil) must not panic on URL in prompt.
func TestGenerate_NilLinksSkipsFetchGracefully(t *testing.T) {
	svc := &Service{gen: &fakeGenerator{visionSupported: true}}

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "https://example.com and also too many images",
		Images: make([]chat.MessageImage, maxImagesPerMessage+1), // fails validation right after, for a cheap assertion
	})
	if !errors.Is(err, ErrTooManyImages) {
		t.Fatalf("expected Generate to proceed past the (skipped) link fetch and fail on the image count check as normal, got %v", err)
	}
}
