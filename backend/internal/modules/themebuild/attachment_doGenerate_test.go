package themebuild

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-chat/internal/ai"
	"ai-chat/internal/modules/chat"
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
