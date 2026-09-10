package themebuild

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/urlfetch"
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

// fakeLinkFetcher stands in for *urlfetch.Fetcher — never makes a real
// network call, matching fakeGenerator's own pattern in
// check_and_repair_test.go.
type fakeLinkFetcher struct {
	calls     int
	lastURL   string
	content   string
	truncated bool
	err       error
}

func (f *fakeLinkFetcher) Fetch(_ context.Context, rawURL string, _ int64) (urlfetch.Result, error) {
	f.calls++
	f.lastURL = rawURL
	if f.err != nil {
		return urlfetch.Result{}, f.err
	}
	return urlfetch.Result{HTML: f.content, Truncated: f.truncated}, nil
}

// blockingLinkFetcher stands in for *urlfetch.Fetcher in tests that need to
// prove Generate itself never calls Fetch — a plain call counter checked
// right after Generate returns would be racing the background goroutine
// Generate spawns to actually run the turn (see runGeneration), which may
// or may not have been scheduled yet. Blocking Fetch on an unbuffered
// channel closes that race outright: if Generate ever called Fetch
// synchronously, the test would hang waiting on release (a much clearer
// failure than a flaky counter check) instead of ever reaching the
// assertions below Generate's own call. The test closes release once it's
// done asserting, letting the real background fetch (if any) proceed so
// runGeneration's goroutine finishes cleanly before the test's DB
// connection is closed by newQueueTestService's own t.Cleanup.
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

// TestGenerate_DoesNotFetchSynchronously is Phase 1's core invariant: a
// prompt containing a URL must not make Generate itself do any network
// work — see Generate's own doc comment on why (POST /chats/messages must
// stay a fast, synchronous 202). The actual fetch happens later, in
// doGenerate, once this turn is dequeued and running in the background
// (see TestDoGenerate_SuccessfulReferenceURLFetch_PersistsForCarryForward).
// This only asserts what Generate carries forward for that later fetch to
// use: the enqueued Generation row's ReferenceURL.
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
	// Reaching this line at all — Fetch would block forever on the unclosed
	// channel if Generate had called it synchronously — is itself part of
	// the proof.
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

// TestGenerate_MalformedURLStillRejectedSynchronously confirms the one
// case that IS still a synchronous 4xx: a URL malformed enough that
// ValidateURL rejects it needs no network call to know that, so there's no
// async-design reason to defer it — see Generate's own doc comment.
// user:pass@ userinfo is one of ValidateURL's own rejection cases (see
// guard_test.go).
func TestGenerate_MalformedURLStillRejectedSynchronously(t *testing.T) {
	svc := &Service{gen: &fakeGenerator{}, links: &fakeLinkFetcher{err: errors.New("must never be called — no network needed for a shape rejection")}}

	_, err := svc.Generate(context.Background(), GenerateInput{
		ThemeSlug: "demo", Prompt: "https://user:pass@example.com can you access this link",
	})
	if !errors.Is(err, ErrLinkFetchFailed) {
		t.Fatalf("expected ErrLinkFetchFailed for a malformed url, got %v", err)
	}
}

// TestGenerate_ExplicitHTMLAttachmentWinsOverLinkInPrompt confirms an
// uploaded HTML file is a more deliberate signal than a URL the merchant
// merely mentioned in the same message — see the link-reference feature's
// own doc comment in Generate. Checked at the point precedence is actually
// decided now: the enqueued row's ReferenceURL must stay empty, not (as
// before Phase 1) a synchronous fetch call that never happens either way.
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

// TestGenerate_NoLinkInPromptSkipsFetch confirms a prompt with no URL at
// all never touches the link fetcher.
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

// TestGenerate_NilLinksSkipsFetchGracefully is the regression this guards:
// a Service built by struct literal without setting links (svc.links stays
// nil, its zero value) — matching how every other test in this file
// constructs one — must not panic on a prompt that happens to contain a
// url; it should behave exactly as if no reference link feature existed at
// all, same as before this feature was added.
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
