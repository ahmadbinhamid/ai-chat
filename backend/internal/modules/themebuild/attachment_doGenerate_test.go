package themebuild

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
	"ai-chat/internal/urlfetch"
)

// capturingGenerator records only the FIRST Generate call's prompt/images, since the
// empty-proposal retry loop resends a different nudge prompt on later calls.
type capturingGenerator struct {
	mu              sync.Mutex
	visionSupported bool
	captured        bool
	firstPrompt     string
	firstImages     []ai.Image
}

func (g *capturingGenerator) Generate(_ context.Context, _ ai.ThemeContext, _ []ai.Turn, prompt string, images []ai.Image, _ func(string), _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	if !g.captured {
		g.captured = true
		g.firstPrompt = prompt
		g.firstImages = images
	}
	g.mu.Unlock()
	return &ai.Result{Summary: "ok"}, nil
}

func (g *capturingGenerator) SupportsVision() bool { return g.visionSupported }

func (g *capturingGenerator) Summarize(_ context.Context, _ []ai.Turn) (string, error) {
	return "", nil
}

func (g *capturingGenerator) snapshot() (bool, string, []ai.Image) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.captured, g.firstPrompt, g.firstImages
}

// allCallsCapturingGenerator records every call's prompt in order, for carry-forward tests
// needing a later turn's prompt; AnsweredQuestion: true avoids the empty-proposal retry loop.
type allCallsCapturingGenerator struct {
	mu      sync.Mutex
	prompts []string
}

func (g *allCallsCapturingGenerator) Generate(_ context.Context, _ ai.ThemeContext, _ []ai.Turn, prompt string, _ []ai.Image, _ func(string), _ ai.ToolProgress, _ ai.ToolExecutor, _ ai.FileReader) (*ai.Result, error) {
	g.mu.Lock()
	g.prompts = append(g.prompts, prompt)
	g.mu.Unlock()
	return &ai.Result{Summary: "ok", AnsweredQuestion: true}, nil
}

func (g *allCallsCapturingGenerator) SupportsVision() bool { return false }

func (g *allCallsCapturingGenerator) Summarize(_ context.Context, _ []ai.Turn) (string, error) {
	return "", nil
}

func (g *allCallsCapturingGenerator) snapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.prompts))
	copy(out, g.prompts)
	return out
}

// waitForSecondAssistantReply is the two-turn counterpart to waitForAssistantReply, for tests
// that send a second prompt to the same chat and need to wait for THAT turn specifically.
func waitForSecondAssistantReply(t *testing.T, chatSvc *chat.Service, tenantID uint64, chatID string) {
	t.Helper()
	const wantReplies = 2
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := chatSvc.ListMessages(context.Background(), tenantID, chatID)
		if err == nil {
			count := 0
			for _, m := range messages {
				if m.Role == chat.RoleAssistant {
					count++
				}
			}
			if count >= wantReplies {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d assistant replies", wantReplies)
}

// waitForAssistantReply waits for the WHOLE background generation to finish, not just the
// first Generate call — returning early leaves runGeneration's goroutine racing the next test.
func waitForAssistantReply(t *testing.T, chatSvc *chat.Service, tenantID uint64, chatID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		messages, err := chatSvc.ListMessages(context.Background(), tenantID, chatID)
		if err == nil {
			for _, m := range messages {
				if m.Role == chat.RoleAssistant {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the background generation to finish (no assistant reply recorded)")
}

// Runs a real prompt with an image and HTML attachment through the full async pipeline and
// asserts on the assembled model input, proving the decode/re-encode round trip works.
func TestDoGenerate_ResolvesAttachmentBytesIntoModelInput(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{visionSupported: true}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	// Validates base64 decode-then-re-encode round trip.
	const wantImageBytes = "hello world"
	imageBase64 := base64.StdEncoding.EncodeToString([]byte(wantImageBytes))
	filename := "reference.html"
	htmlContent := "<h1>Match this structure</h1>"

	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "redesign the homepage",
		Images:                 []chat.MessageImage{{Base64: imageBase64, MediaType: "image/png"}},
		HTMLAttachmentFilename: &filename,
		HTMLAttachmentContent:  &htmlContent,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)
	captured, gotPrompt, gotImages := gen.snapshot()

	if !captured {
		t.Fatal("expected the background generation to have called gen.Generate")
	}

	if len(gotImages) != 1 {
		t.Fatalf("expected 1 image in the assembled model input, got %d: %+v", len(gotImages), gotImages)
	}
	if gotImages[0].MediaType != "image/png" {
		t.Errorf("expected media type %q, got %q", "image/png", gotImages[0].MediaType)
	}
	gotBytes, err := base64.StdEncoding.DecodeString(gotImages[0].Base64)
	if err != nil {
		t.Fatalf("model input's image base64 didn't decode: %v", err)
	}
	if string(gotBytes) != wantImageBytes {
		t.Errorf("expected the model to receive image bytes %q, got %q", wantImageBytes, string(gotBytes))
	}

	if !strings.Contains(gotPrompt, filename) {
		t.Errorf("expected the assembled prompt to name the attached file %q, got: %s", filename, gotPrompt)
	}
	if !strings.Contains(gotPrompt, htmlContent) {
		t.Errorf("expected the assembled prompt to include the attached HTML content, got: %s", gotPrompt)
	}
}

// A text-only prompt must produce a model input with no images and an unmodified prompt.
func TestDoGenerate_ZeroAttachmentMessage_SkipsContentFetch(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{visionSupported: true}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme", Prompt: "make the header blue",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)
	captured, gotPrompt, gotImages := gen.snapshot()

	if !captured {
		t.Fatal("expected the background generation to have called gen.Generate")
	}
	if len(gotImages) != 0 {
		t.Errorf("expected no images in the model input for a text-only prompt, got %+v", gotImages)
	}
	if strings.Contains(gotPrompt, "Attached reference file") {
		t.Errorf("expected no HTML-attachment framing in the prompt for a text-only turn, got: %s", gotPrompt)
	}
	if gotPrompt != "make the header blue" {
		t.Errorf("expected the prompt to pass through unmodified, got: %s", gotPrompt)
	}
}

// Core bug this feature fixes: turn 2 asking to build from turn 1's attachment without
// re-attaching must carry the actual page forward, not just the model's earlier summary.
func TestDoGenerate_CarriesForwardHTMLAttachmentFromEarlierTurn(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	filename := "reference.html"
	htmlContent := "<h1>Match this structure</h1>"

	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                 "here's a reference design",
		HTMLAttachmentFilename: &filename, HTMLAttachmentContent: &htmlContent,
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "build it like that",
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	second := prompts[1]

	if !strings.Contains(second, htmlContent) {
		t.Errorf("expected turn 2's prompt to carry forward turn 1's HTML content, got: %s", second)
	}
	if !strings.Contains(second, filename) {
		t.Errorf("expected turn 2's prompt to name the carried-forward file %q, got: %s", filename, second)
	}
	if !strings.Contains(second, "EARLIER message in this conversation") {
		t.Errorf("expected turn 2's prompt to carry the earlier-turn framing, got: %s", second)
	}
	if strings.Contains(second, "fetched this page's live content on your behalf") {
		t.Errorf("expected NO external-link framing for a carried-forward UPLOAD, got: %s", second)
	}
}

// Same carry-forward path but from a fetched link; the prompt must keep the external-link
// framing too, not just the generic carry-forward one.
func TestDoGenerate_CarriesForwardLinkAttachment_PreservesLinkFraming(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	url := "https://example.com/inspiration"
	htmlContent := "<h1>Inspiration site</h1>"

	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                       url + " can you access this link?",
		HTMLAttachmentFilename:       &url,
		HTMLAttachmentContent:        &htmlContent,
		HTMLAttachmentIsExternalLink: true,
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "yes, use it as a design reference and build it",
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	second := prompts[1]

	for _, want := range []string{
		htmlContent,
		"EARLIER message in this conversation",
		"still the active reference",
		"fetched this page's live content on your behalf",
		"you DID access it",
	} {
		if !strings.Contains(second, want) {
			t.Errorf("expected turn 2's prompt to contain %q, got: %s", want, second)
		}
	}
}

// exactLengthFiller returns a string of exactly n bytes using an alternating "a "/space
// pattern — a plain repeated character would get stripped by the 400+-char base64-run filter.
func exactLengthFiller(n int) string {
	if n <= 0 {
		return ""
	}
	const unit = "a "
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}

// This and the test below cover the regression where comparing a link's digest length
// against the ~300KB upload cap instead of the 16KB digest cap silently lost the truncation note.
func TestDoGenerate_CarryForwardDigestAtHardCap_ReportsTruncated(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	url := "https://example.com/big-reference"
	// Exactly at urlfetch.DigestHardCapBytes to test truncation window.
	htmlContent := exactLengthFiller(urlfetch.DigestHardCapBytes)

	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                       url + " can you access this link?",
		HTMLAttachmentFilename:       &url,
		HTMLAttachmentContent:        &htmlContent,
		HTMLAttachmentIsExternalLink: true,
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "build it like that",
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	if !strings.Contains(prompts[1], "cut short") {
		t.Errorf("expected turn 2's carried-forward prompt to report the digest as truncated, got: %s", prompts[1])
	}
}

func TestDoGenerate_CarryForwardDigestJustUnderTolerance_ReportsNotTruncated(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	url := "https://example.com/normal-reference"
	// One byte below digestTruncatedLengthTolerance's window — must NOT
	// read as truncated.
	htmlContent := exactLengthFiller(urlfetch.DigestHardCapBytes - digestTruncatedLengthTolerance - 1)

	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                       url + " can you access this link?",
		HTMLAttachmentFilename:       &url,
		HTMLAttachmentContent:        &htmlContent,
		HTMLAttachmentIsExternalLink: true,
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "build it like that",
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	if strings.Contains(prompts[1], "cut short") {
		t.Errorf("expected turn 2's carried-forward prompt to NOT report the digest as truncated, got: %s", prompts[1])
	}
}

// A turn's own explicit HTML attachment must always win over a carried-forward one.
func TestDoGenerate_CurrentTurnAttachmentWinsOverCarryForward(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	firstFilename := "first-reference.html"
	firstContent := "<h1>First reference — should not resurface</h1>"

	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                 "here's a reference design",
		HTMLAttachmentFilename: &firstFilename, HTMLAttachmentContent: &firstContent,
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	secondFilename := "second-reference.html"
	secondContent := "<h1>Second reference — this turn's own</h1>"
	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt:                 "actually use this one instead",
		HTMLAttachmentFilename: &secondFilename, HTMLAttachmentContent: &secondContent,
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	second := prompts[1]

	if !strings.Contains(second, secondContent) {
		t.Errorf("expected turn 2's prompt to use its OWN attachment content, got: %s", second)
	}
	if strings.Contains(second, firstContent) {
		t.Errorf("expected turn 2's prompt to NOT carry forward turn 1's attachment when it has its own, got: %s", second)
	}
	if strings.Contains(second, "EARLIER message in this conversation") {
		t.Errorf("expected no carry-forward framing when the current turn has its own attachment, got: %s", second)
	}
}

// A reference-URL fetch failing must still produce a completed turn, with the prompt saying
// so plainly rather than proceeding as if no link had been mentioned.
func TestDoGenerate_ReferenceURLFetchFailureDoesNotFailTurn(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{}
	svc.gen = gen
	svc.links = &fakeLinkFetcher{err: urlfetch.ErrBlockedHost}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "https://example.com can you access this link",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	var assistant *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistant = &messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("expected an assistant reply despite the fetch failure")
	}
	if assistant.Status != chat.MessageStatusCompleted {
		t.Errorf("expected a completed turn despite the fetch failure, got status %q", assistant.Status)
	}

	captured, gotPrompt, _ := gen.snapshot()
	if !captured {
		t.Fatal("expected the background generation to have called gen.Generate")
	}
	if !strings.Contains(gotPrompt, "could not reach or read it") {
		t.Errorf("expected the prompt to include the could-not-fetch note, got: %s", gotPrompt)
	}
	if strings.Contains(gotPrompt, "you DID access it") {
		t.Errorf("expected NO you-DID-access framing on a failed fetch, got: %s", gotPrompt)
	}
}

// Production regression: a turn whose OWN reference URL fetch fails must report that failure
// honestly, not silently fall back to carrying forward an earlier, unrelated turn's page.
func TestDoGenerate_CurrentTurnFailedReferenceURL_DoesNotFallBackToEarlierTurn(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen
	fl := &fakeLinkFetcher{content: fetchReferenceURLTestHTML}
	svc.links = fl

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "https://coconut-water-site.example.com can you access this link",
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	// Safe to flip the shared fake here: turn 1's fetch already happened above.
	fl.err = errors.New("simulated fetch failure for turn 2's own URL")

	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "redesign the homepage exactly similar to the attached link https://ebay-lookalike.example.com",
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	second := prompts[1]

	if strings.Contains(second, "EARLIER message in this conversation") {
		t.Errorf("expected NO carry-forward framing when turn 2 had its own (failed) reference URL, got: %s", second)
	}
	if strings.Contains(second, "cached") {
		// Turn 1's page content must not leak into turn 2.
		t.Errorf("expected turn 1's carried-forward content to NOT appear in turn 2's prompt, got: %s", second)
	}
	if !strings.Contains(second, "could not reach or read it") {
		t.Errorf("expected turn 2's prompt to honestly report ITS OWN fetch failure, got: %s", second)
	}
}

// A successful reference-URL fetch must persist as a real chat_message_attachments row, not
// just live in local GenerateInput scope, or a later turn's carry-forward would find nothing.
func TestDoGenerate_SuccessfulReferenceURLFetch_PersistsForCarryForward(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen
	svc.links = &fakeLinkFetcher{content: "<h1>Reference Site</h1>"}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "https://example.com can you access this link",
	})
	if err != nil {
		t.Fatalf("first Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	_, err = svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "build it like that",
	})
	if err != nil {
		t.Fatalf("second Generate failed: %v", err)
	}
	waitForSecondAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	prompts := gen.snapshot()
	if len(prompts) != 2 {
		t.Fatalf("expected exactly 2 Generate calls (one per turn), got %d: %+v", len(prompts), prompts)
	}
	first, second := prompts[0], prompts[1]
	// Turn 1's fetch produces digest, not raw markup.
	if !strings.Contains(first, "Reference Site") {
		t.Errorf("expected turn 1 to contain the fetched page's extracted content, got: %s", first)
	}
	if strings.Contains(first, "<h1>") {
		t.Errorf("expected turn 1's content to be a digest, not raw markup, got: %s", first)
	}
	// Fetched pages carry forward as digests, not raw markup.
	if !strings.Contains(second, "Reference Site") {
		t.Errorf("expected turn 2 to carry forward the fetched page's extracted content, got: %s", second)
	}
	if strings.Contains(second, "<h1>") {
		t.Errorf("expected turn 2's carried-forward content to be a digest, not raw markup, got: %s", second)
	}
	if !strings.Contains(second, "EARLIER message in this conversation") {
		t.Errorf("expected turn 2's prompt to carry the earlier-turn framing, got: %s", second)
	}
}

// Verifies truncation to 255 chars for storage while keeping full URL in prompt.
// Critical: "https://" prefix must survive for carry-forward classification.
func TestDoGenerate_LongReferenceURL_TruncatesStoredFilenameButKeepsFullURLInPrompt(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{}
	svc.gen = gen
	svc.links = &fakeLinkFetcher{content: "<h1>Long URL page</h1>"}

	longURL := "https://example.com/" + strings.Repeat("a", 300)
	if len(longURL) <= 255 {
		t.Fatalf("test setup: longURL must be over 255 chars, got %d", len(longURL))
	}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: longURL + " can you access this link",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	captured, gotPrompt, _ := gen.snapshot()
	if !captured {
		t.Fatal("expected the background generation to have called gen.Generate")
	}
	if !strings.Contains(gotPrompt, longURL) {
		t.Errorf("expected the FULL long URL in the model prompt, got: %s", gotPrompt)
	}

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	var userMsg *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleUser {
			userMsg = &messages[i]
		}
	}
	if userMsg == nil || len(userMsg.Attachments) == 0 {
		t.Fatal("expected the user message to have a persisted HTML attachment")
	}
	storedFilename := userMsg.Attachments[0].Filename
	if len(storedFilename) > 255 {
		t.Errorf("expected the stored filename to be capped at 255 chars, got %d", len(storedFilename))
	}
	if !looksLikeFetchedLink(storedFilename) {
		t.Errorf("expected looksLikeFetchedLink to still match the truncated filename %q", storedFilename)
	}
}

// Verifies truncation not failure for links exceeding digestHardCapBytes (16KB).
// Phase 3: digests instead of raw HTML; same behavioral guarantee as Phase 2.
func TestDoGenerate_LinkOverDigestHardCap_TruncatesInsteadOfFailing(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{}
	svc.gen = gen

	var html strings.Builder
	html.WriteString("<html><head><title>Big Page</title></head><body>")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&html, "<h2>Section heading number %d with enough padding text to push each heading close to the "+
			"two hundred character per-heading cap so that forty of these alone already approach eight kilobytes "+
			"on their own before any other section is even considered at all</h2>", i)
	}
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&html, "<section><p>Landmark section number %d contains a long paragraph of filler copy repeated "+
			"specifically to fill out its one-line preview all the way to the landmark preview character cap of "+
			"one hundred and twenty characters so the structure section alone contributes a meaningful chunk of "+
			"the sixteen kilobyte hard cap on its own merits.</p></section>", i)
	}
	html.WriteString("<p>" + strings.Repeat(
		"Body copy filler text meant to fill the general copy section all the way up to its own two thousand character cap. ",
		40,
	) + "</p>")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&html, `<a href="/link%d">Interactive label number %d padded out toward the sixty character per label cap so</a>`, i, i)
	}
	html.WriteString("</body></html>")
	bigContent := html.String()

	// HTML alone lands just under digestHardCapBytes; CSS design tokens push it over.
	var css strings.Builder
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&css, "--token-%d: value-%d-padded-out-toward-the-two-hundred-character-cap-just-a-little-bit-more-text-here-to-make-it-longer;\n", i, i)
	}
	bigCSS := ":root {\n" + css.String() + "}\n"

	svc.links = &fakeLinkFetcher{content: bigContent, stylesheetCSS: bigCSS, stylesheetCount: 1}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "https://example.com can you access this link",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	var assistant *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistant = &messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("expected an assistant reply for an oversized link (truncated, not rejected)")
	}
	if assistant.Status != chat.MessageStatusCompleted {
		t.Errorf("expected a completed turn, got status %q", assistant.Status)
	}

	captured, gotPrompt, _ := gen.snapshot()
	if !captured {
		t.Fatal("expected the background generation to have called gen.Generate")
	}
	if !strings.Contains(gotPrompt, "cut short") {
		t.Errorf("expected the truncation note in the prompt, got: %s", gotPrompt)
	}
}

// SPA pages that digest to nothing (all scripts) must be treated as fetch failures,
// not successes with empty attachments.
func TestDoGenerate_LinkSanitizesToEmpty_TreatedAsFetchFailureNotSuccess(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{}
	svc.gen = gen
	// Page is all scripts, digests to nothing.
	svc.links = &fakeLinkFetcher{content: `<script>document.body.innerHTML = renderApp();</script>`}

	tenantID := uint64(time.Now().UnixNano())
	outcome, err := svc.Generate(context.Background(), GenerateInput{
		TenantID: tenantID, UserID: &tenantID, Token: "t", ThemeSlug: "theme",
		Prompt: "https://example.com can you access this link",
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	waitForAssistantReply(t, chatSvc, tenantID, outcome.Chat.ID)

	messages, err := chatSvc.ListMessages(context.Background(), tenantID, outcome.Chat.ID)
	if err != nil {
		t.Fatalf("failed to load messages: %v", err)
	}
	var assistant *chat.Message
	for i := range messages {
		if messages[i].Role == chat.RoleAssistant {
			assistant = &messages[i]
		}
	}
	if assistant == nil {
		t.Fatal("expected an assistant reply even though the page sanitized to nothing")
	}
	if assistant.Status != chat.MessageStatusCompleted {
		t.Errorf("expected a completed turn, got status %q", assistant.Status)
	}

	captured, gotPrompt, _ := gen.snapshot()
	if !captured {
		t.Fatal("expected the background generation to have called gen.Generate")
	}
	if !strings.Contains(gotPrompt, "JavaScript") {
		t.Errorf("expected the JS-rendered-content note in the prompt, got: %s", gotPrompt)
	}
	if strings.Contains(gotPrompt, "fetched this page's live content on your behalf") {
		t.Errorf("expected the fetch-failure framing, NOT the external-link success framing, got: %s", gotPrompt)
	}
}
