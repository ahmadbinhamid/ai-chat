package ai

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// TestVisionModelSmokeTest is a throwaway go/no-go check for whether the vision model can
// drive this project's real tool-loop with an image attached. Skipped unless
// DEEPSEEK_VISION_SMOKE_TEST=1 — hits a real, billed API, no place in normal `make test`/CI.
func TestVisionModelSmokeTest(t *testing.T) {
	if os.Getenv("DEEPSEEK_VISION_SMOKE_TEST") != "1" {
		t.Skip("set DEEPSEEK_VISION_SMOKE_TEST=1 to run this against the real DeepSeek API")
	}
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Fatal("DEEPSEEK_API_KEY not set")
	}
	baseURL := os.Getenv("DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/anthropic"
	}
	visionModel := os.Getenv("DEEPSEEK_VISION_MODEL")
	if visionModel == "" {
		visionModel = "deepseek-v4-flash-vision-exp"
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL))

	// A real, valid, minimal 1x1 PNG — this is about pipeline compatibility, not vision quality.
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

	tools := []anthropic.ToolUnionParam{
		listThemeFilesTool(),
		readThemeFileTool(),
		grepThemeTool(),
		proposeChangesTool(),
	}

	run := func(t *testing.T, name string, withThinking bool) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			params := anthropic.MessageNewParams{
				Model:     visionModel,
				MaxTokens: 4096,
				Tools:     tools,
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(
						anthropic.NewImageBlockBase64("image/png", onePixelPNG),
						anthropic.NewTextBlock(
							"Describe this image in one sentence, then call propose_changes with "+
								"needs_clarification: true, files: [], and a summary explaining you were "+
								"just asked to describe a test image and there's nothing to change.",
						),
					),
				},
			}
			if withThinking {
				params.Thinking = anthropic.ThinkingConfigParamUnion{OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{}}
				params.OutputConfig = anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortMedium}
			}

			msg, err := client.Messages.New(ctx, params)
			if err != nil {
				t.Fatalf("API call failed (withThinking=%v): %v", withThinking, err)
			}

			t.Logf("stop_reason=%s usage=%+v", msg.StopReason, msg.Usage)

			var sawProposeChanges bool
			var toolNames []string
			for _, block := range msg.Content {
				switch b := block.AsAny().(type) {
				case anthropic.TextBlock:
					t.Logf("text block: %s", b.Text)
				case anthropic.ToolUseBlock:
					toolNames = append(toolNames, b.Name)
					if b.Name == toolNameProposeChanges {
						sawProposeChanges = true
						var result Result
						if err := json.Unmarshal(b.Input, &result); err != nil {
							t.Errorf("propose_changes input didn't parse as Result: %v\nraw: %s", err, string(b.Input))
						} else {
							t.Logf("parsed propose_changes: needs_clarification=%v summary=%q files=%d",
								result.NeedsClarification, result.Summary, len(result.Files))
						}
					}
				}
			}

			t.Logf("tools called: %v", toolNames)
			if !sawProposeChanges {
				t.Errorf("GO/NO-GO FAIL: model never called propose_changes (tools called: %v) — "+
					"it likely replied conversationally instead of using the tool loop", toolNames)
			}
		})
	}

	run(t, "without_thinking", false)
	run(t, "with_thinking_and_effort", true)
}

// TestVisionModel_ImageTokenDelta isolates the image's marginal input-token cost — same
// model/tools/prompt, differing only in whether an image block is attached.
func TestVisionModel_ImageTokenDelta(t *testing.T) {
	if os.Getenv("DEEPSEEK_VISION_SMOKE_TEST") != "1" {
		t.Skip("set DEEPSEEK_VISION_SMOKE_TEST=1 to run this against the real DeepSeek API")
	}
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Fatal("DEEPSEEK_API_KEY not set")
	}
	baseURL := os.Getenv("DEEPSEEK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/anthropic"
	}
	visionModel := os.Getenv("DEEPSEEK_VISION_MODEL")
	if visionModel == "" {
		visionModel = "deepseek-v4-flash-vision-exp"
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL))
	const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	const prompt = "Say hello in one word, then call propose_changes with needs_clarification: true, files: [], and a one-line summary."
	tools := []anthropic.ToolUnionParam{proposeChangesTool()}

	call := func(t *testing.T, withImage bool) int64 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		var blocks []anthropic.ContentBlockParamUnion
		if withImage {
			blocks = append(blocks, anthropic.NewImageBlockBase64("image/png", onePixelPNG))
		}
		blocks = append(blocks, anthropic.NewTextBlock(prompt))
		msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     visionModel,
			MaxTokens: 1024,
			Tools:     tools,
			Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(blocks...)},
		})
		if err != nil {
			t.Fatalf("API call failed (withImage=%v): %v", withImage, err)
		}
		t.Logf("withImage=%v input_tokens=%d output_tokens=%d", withImage, msg.Usage.InputTokens, msg.Usage.OutputTokens)
		return msg.Usage.InputTokens
	}

	// Order matters: DeepSeek caches on request-prefix match, so whichever call runs SECOND
	// can get a discounted reading regardless of the image. Order is reversed here to check
	// for that confound: if "second call is cheaper" flips with the order, it's a caching artifact.
	withImage := call(t, true)
	withoutImage := call(t, false)
	t.Logf("GO/NO-GO (reversed order): image's own marginal input-token cost = %d tokens", withImage-withoutImage)
}
