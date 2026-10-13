package themebuild

import (
	"strings"
	"testing"

	"ai-chat/internal/prefetch"
	"ai-chat/internal/previewerrors"
)

// The merchant's request must come last, right before the tool instruction, whatever else the turn carries.
func TestPromptWithAttachments_RequestIsLast(t *testing.T) {
	filename, content := "ref.html", "<h1>REF</h1>"
	preload := prefetch.Result{Files: []prefetch.File{{Path: "pages/home.liquid", Content: "<h1>HOME</h1>"}}}
	tests := []struct {
		name  string
		in    GenerateInput
		order []string
	}{
		{"request only", GenerateInput{}, []string{"REQUEST", proposeInstruction}},
		{"preload first", GenerateInput{preload: preload}, []string{preloadHeading, "<h1>HOME</h1>", "REQUEST", proposeInstruction}},
		{"everything", GenerateInput{
			preload: preload, HTMLAttachmentFilename: &filename, HTMLAttachmentContent: &content,
			PreviewErrors: samplePreviewErrors,
		}, []string{preloadHeading, "--- Attached reference file: ref.html", "<h1>REF</h1>", "--- Browser errors", "REQUEST", proposeInstruction}},
		{"a failed link fetch is a note before the request", GenerateInput{ReferenceURL: "https://x.test", ReferenceURLFetchFailed: true},
			[]string{"The platform tried to fetch https://x.test", "REQUEST", proposeInstruction}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := promptWithAttachments("REQUEST", tt.in)
			if !strings.HasSuffix(got, "REQUEST\n\n"+proposeInstruction) {
				t.Fatalf("the turn must end with the request and the instruction:\n%s", got)
			}
			at := -1
			for _, part := range tt.order {
				i := strings.Index(got, part)
				if i <= at {
					t.Fatalf("%q is out of order (at %d, previous part at %d):\n%s", part, i, at, got)
				}
				at = i
			}
		})
	}
}

// A flat repair fallback rebuilds the turn from here, so it keeps the same order with its own text last.
func TestPromptWithAttachments_FlatRepairKeepsOrder(t *testing.T) {
	in := GenerateInput{Prompt: "Invalid base URL", preload: prefetch.Result{Files: []prefetch.File{{Path: "a.liquid", Content: "A"}}}}
	got := promptWithAttachments("Your last proposal failed validation", in)
	if !strings.HasPrefix(got, preloadHeading) || !strings.HasSuffix(got, "Your last proposal failed validation\n\n"+proposeInstruction) {
		t.Fatalf("repair turn out of order:\n%s", got)
	}
	if !strings.Contains(got, previewerrors.SandboxErrorNote) {
		t.Fatal("the repair turn lost the merchant-message notes")
	}
}
