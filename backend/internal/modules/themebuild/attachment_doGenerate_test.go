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

// capturingGenerator records the prompt/images its FIRST Generate call
// received, and ignores every call after — unlike scriptedGenerator/
// fakeGenerator (both of which discard these entirely to focus on other
// behavior), this is specifically for asserting on the ASSEMBLED model
// input doGenerate builds for a turn's original prompt, not internals. A
// trivial "ok" summary with no files/exploration reliably triggers
// generateValidProposal's own empty-proposal retry loop, which resends a
// DIFFERENT, nudge-only prompt ("Please try again...") on later calls — so
// capturing only the first call (not "whatever's there when polled", and
// not the most recent) is what makes an assertion about the ORIGINAL
// prompt/attachments deterministic. See
// TestDoGenerate_ResolvesAttachmentBytesIntoModelInput.
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

// allCallsCapturingGenerator records every Generate call's prompt, in
// order — unlike capturingGenerator (which only keeps the first, for
// asserting on a single turn's assembled input), the carry-forward tests
// below send more than one turn to the same chat and need to inspect a
// LATER turn's prompt specifically. Always returns AnsweredQuestion: true
// so generateValidProposal accepts it immediately — see
// isUnexploredEmptyProposal's own doc comment: a plain "ok" summary with no
// files and no exploration otherwise looks like a suspected-hallucination
// empty proposal and triggers a retry loop, which would make the number of
// captured prompts per turn nondeterministic instead of exactly one.
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

// waitForSecondAssistantReply polls until chatID has at least two
// assistant-role messages — the two-turn counterpart to
// waitForAssistantReply (see its own doc comment for why waiting for real
// completion, not just the generator having been called, matters), for
// tests that send a second prompt to the same chat (e.g. the carry-forward
// tests below) and need to wait for THAT turn specifically, not just any
// assistant reply.
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

// waitForAssistantReply polls until chatID has at least one assistant-role
// message — proof the WHOLE background generation (not just its first
// Generate call) has finished, including whatever runs after: retries,
// event recording, draft staging. Every test in this file waits on this,
// not just on capturingGenerator having seen a call — returning early would
// leave runGeneration's goroutine still running against openTestDB's
// connection after t.Cleanup closes it (see openTestDB), which doesn't just
// log noise: the still-running goroutine keeps hitting MySQL and racing
// with whatever test runs next in this package, which is exactly what made
// unrelated timing-sensitive tests (TestRunGeneration_DrainsWholeQueueInOrder
// et al) flake when this file's tests didn't wait for real completion.
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

// TestDoGenerate_ResolvesAttachmentBytesIntoModelInput runs a real prompt
// with one image and one HTML attachment all the way through the actual
// async pipeline (Generate -> RecordUserMessage -> queue -> doGenerate's
// re-resolution -> generateValidProposal) against a real database, and
// asserts on the model input doGenerate actually assembled — not on any
// internal representation along the way. This is the round-trip that
// matters: base64 in the request, decoded to raw bytes for storage,
// re-encoded to base64 only transiently when calling the model, and the
// HTML folded into the prompt text exactly as promptWithHTMLAttachment
// always has.
func TestDoGenerate_ResolvesAttachmentBytesIntoModelInput(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{visionSupported: true}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	// "hello world", base64-encoded WITH padding — a passing assertion
	// that the model receives this exact base64 back proves a real
	// decode-then-re-encode round trip happened, not a pass-through.
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

// TestDoGenerate_ZeroAttachmentMessage_SkipsContentFetch is the
// zero-attachments edge case at the doGenerate boundary: a plain
// text-only prompt still runs the whole real pipeline, and must produce a
// model input with no images and an unmodified prompt (no "Attached
// reference file" framing) — proving the re-resolution block's
// len(m.Attachments) == 0 guard actually short-circuits rather than
// something further down silently tolerating empty attachment data.
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

// TestDoGenerate_CarriesForwardHTMLAttachmentFromEarlierTurn is the core
// bug this feature fixes: turn 1 attaches an uploaded HTML reference, turn
// 2 asks to build from it without re-attaching anything. Before the
// carry-forward fallback, turn 2's assembled prompt had zero page content —
// the model designed from its own earlier summary, not the actual page.
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

// TestDoGenerate_CarriesForwardLinkAttachment_PreservesLinkFraming is the
// same carry-forward path, but the source attachment came from a fetched
// link (filename is a URL) rather than an upload — the carried-forward
// prompt must keep the external-link framing too, not just the generic one.
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

// exactLengthFiller returns a string of EXACTLY n bytes, built from an
// alternating "a "/space pattern rather than a single repeated character —
// SanitizeHTMLAttachment's longBase64RunRe strips any run of 400+
// consecutive characters from the base64 alphabet (which includes plain
// lowercase letters), so a naive strings.Repeat("a", n) would silently be
// stripped to "" by the upload-path validation these carry-forward tests
// route through (see Generate's own HTMLAttachmentContent handling) before
// ever reaching the length this test needs to control precisely. The
// space breaks every run at length 1, so nothing here matches that regex.
func exactLengthFiller(n int) string {
	if n <= 0 {
		return ""
	}
	const unit = "a "
	return strings.Repeat(unit, n/len(unit)+1)[:n]
}

// TestDoGenerate_CarryForwardDigestAtHardCap_ReportsTruncated and
// TestDoGenerate_CarryForwardDigestJustUnderTolerance_ReportsNotTruncated
// cover the regression Phase 3 introduced and Phase 3.1 fixes:
// looksTruncatedByStoredLength used to compare EVERY stored HTML
// attachment's length against PostStripMaxBytes (~300KB) regardless of
// kind, but Phase 3 changed a link's stored content from sanitized raw
// markup to a digest capped at urlfetch.DigestHardCapBytes (16KB) — a
// comparison against ~300KB could never be true for a digest, so a
// carried-forward truncated link silently lost its "this copy was cut
// short" note. Driven through a REAL carry-forward turn, not just the
// helper in isolation, matching how the loss actually showed up: a
// merchant building from a reference on turn 2 with no idea turn 1's
// fetch was cut short.
func TestDoGenerate_CarryForwardDigestAtHardCap_ReportsTruncated(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &allCallsCapturingGenerator{}
	svc.gen = gen

	tenantID := uint64(time.Now().UnixNano())
	url := "https://example.com/big-reference"
	// Exactly at urlfetch.DigestHardCapBytes — squarely inside
	// digestTruncatedLengthTolerance's window, so this must read as
	// truncated.
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

// TestDoGenerate_CurrentTurnAttachmentWinsOverCarryForward confirms a turn
// that attaches its OWN HTML reference never gets the carried-forward one
// instead — the current turn's explicit attachment must always win, with no
// carry-forward framing leaking in alongside it.
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

// TestDoGenerate_ReferenceURLFetchFailureDoesNotFailTurn confirms Phase 1's
// graceful-degradation contract: a reference-URL fetch failing inside
// doGenerate must still produce a completed turn (the merchant asked a
// real question and deserves an answer about everything except the page —
// see GenerateInput.ReferenceURLFetchFailed's own doc comment), and the
// prompt handed to the model must say so plainly rather than silently
// proceeding as if no link had ever been mentioned.
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

// TestDoGenerate_CurrentTurnFailedReferenceURL_DoesNotFallBackToEarlierTurn
// covers a real observed production bug: a merchant references one site on
// turn 1 (which fetches fine), then on turn 2 names a DIFFERENT site of
// their own — whose fetch fails. Before this fix, the carry-forward
// fallback (see doGenerate's own comment on it) ran whenever
// HTMLAttachmentContent was nil, with no check for WHY it was nil — so a
// turn with its own (just-failed) reference URL fell straight through to
// carrying forward turn 1's completely unrelated page, with no indication
// to the model (and so the merchant) that the new URL was ever tried at
// all. The fix requires in.ReferenceURL == "" too: carry-forward is for "no
// reference this turn," not "this turn's own reference didn't work out" —
// the latter must surface as an honest failure note (see
// TestDoGenerate_ReferenceURLFetchFailureDoesNotFailTurn above), never a
// silent substitution.
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

	// Turn 2 names its OWN, different URL — and that one's fetch fails.
	// Flipping the shared fake's behavior here is safe: turn 1 has already
	// completed (waitForAssistantReply above), so turn 1's own fetch call
	// already happened against the old (successful) configuration.
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
		// fetchReferenceURLTestHTML's own distinguishing heading text
		// ("cached") — proves turn 1's unrelated page content did not leak
		// into turn 2's prompt.
		t.Errorf("expected turn 1's carried-forward content to NOT appear in turn 2's prompt, got: %s", second)
	}
	if !strings.Contains(second, "could not reach or read it") {
		t.Errorf("expected turn 2's prompt to honestly report ITS OWN fetch failure, got: %s", second)
	}
}

// TestDoGenerate_SuccessfulReferenceURLFetch_PersistsForCarryForward covers
// Phase 1's persistence requirement end to end: a reference-URL fetch that
// succeeds inside doGenerate must write the fetched content as a real
// chat_message_attachments row (see chat.Service.AttachHTMLToMessage), not
// just hold it in local GenerateInput scope for this one turn — otherwise
// findCarryForwardSourceMessageID would find nothing on a later turn, since
// RecordUserMessage itself no longer sees fetched content (only Generate's
// pre-fetch URL detection).
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
	// Turn 1's OWN fetch — not carry-forward — produces a prompt with the
	// digest, not raw markup: the heading's TEXT ("Reference Site")
	// survives extraction, but the literal "<h1>" tag does not.
	if !strings.Contains(first, "Reference Site") {
		t.Errorf("expected turn 1 to contain the fetched page's extracted content, got: %s", first)
	}
	if strings.Contains(first, "<h1>") {
		t.Errorf("expected turn 1's content to be a digest, not raw markup, got: %s", first)
	}
	// The fetched page is carried forward as its DIGEST, not raw markup
	// (see Service.fetchReferenceURL) — the heading's TEXT ("Reference
	// Site") survives extraction, but the literal "<h1>" tag does not.
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

// TestDoGenerate_LongReferenceURL_TruncatesStoredFilenameButKeepsFullURLInPrompt
// covers the VARCHAR(255) filename column vs. urlfetch's 2048-char maxURLLen
// mismatch: a URL over 255 chars must still round-trip in full to the
// model (via GenerateInput.HTMLAttachmentFilename, never touched by the
// truncation), while the persisted chat_message_attachments row's filename
// is capped to fit the column — and must still look like a fetched link to
// looksLikeFetchedLink after that truncation (i.e. the "https://" prefix
// survives), or a later turn's carry-forward would misclassify it as an
// upload.
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

// TestDoGenerate_LinkOverDigestHardCap_TruncatesInsteadOfFailing covers
// truncate-not-fail for a link's DIGEST now, not its raw fetched HTML —
// Phase 3 (see urlfetch.BuildDigest) replaced a link's raw sanitized
// markup with a compact structured digest, capped at digestHardCapBytes
// (16KB), well under MaxHTMLAttachmentBytes (PostStripMaxBytes, 300KB).
// That makes the OLD version of this test's premise (a post-sanitize
// result still over PostStripMaxBytes) effectively unreachable for a real
// page — see Service.fetchReferenceURL's own doc comment on
// PostStripMaxBytes now being a backstop, not the normal path — so this
// exercises the truncation BuildDigest itself actually performs instead: a
// page with far more extractable headings/landmarks/copy than fits in
// 16KB still completes the turn, with the digest cut down (see
// Digest.Truncated) rather than the reference being rejected. Still
// exactly the same behavioral guarantee Phase 2 first established (an
// oversized link truncates, it never fails the turn) — see
// TestGenerate_HTMLAttachmentStillTooLargeAfterStripping, unchanged, for
// the upload path's own, still-different, still-hard-rejecting behavior.
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

	// Every per-section cap above (40 headings, 20 landmarks, 2000-char
	// copy, 40 labels) is already saturated by the HTML alone, but that
	// combination alone lands just under digestHardCapBytes — a
	// DESIGN TOKENS section (from CSS, which a real fetch would also
	// collect via FetchStylesheets) is what pushes the total over, the
	// same way it did for urlfetch's own over_hard_cap fixture.
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

// TestDoGenerate_LinkSanitizesToEmpty_TreatedAsFetchFailureNotSuccess covers
// the client-rendered-SPA case: a page whose entire server-sent HTML is a
// single <script> block digests to nothing (no headings, no landmarks, no
// copy — see Digest.Empty's own doc comment; extraction excludes script
// content the same way SanitizeHTMLAttachment used to strip it outright).
// This must take the ReferenceURLFetchFailed path with the distinct
// JS-rendered note (see promptWithHTMLAttachment), NOT the external-link
// success framing with an empty attachment — the model has nothing to
// actually read from the page, and the success framing would make it
// falsely claim otherwise.
func TestDoGenerate_LinkSanitizesToEmpty_TreatedAsFetchFailureNotSuccess(t *testing.T) {
	svc, chatSvc := newQueueTestService(t)
	gen := &capturingGenerator{}
	svc.gen = gen
	// Digests to "" in full: the whole body is one <script> block, which
	// extraction excludes entirely, and nothing else in the page survives
	// to take its place.
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
