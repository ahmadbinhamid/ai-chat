package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMessage_JSON_OmitsAttachmentContent is the regression test for "never
// ship a turn's attached bytes to the browser": MessageAttachment.Content
// is json:"-" (see its own doc comment), so a marshaled Message must expose
// only metadata (id/kind/filename/media_type/size_bytes/position) for each
// attachment, never Content, no matter how it's set.
func TestMessage_JSON_OmitsAttachmentContent(t *testing.T) {
	imageContent := []byte("this exact image byte sequence must never appear in the JSON output")
	htmlContent := []byte("<h1>this exact HTML byte sequence must never appear in the JSON output</h1>")
	m := Message{
		ID:      "msg-1",
		Role:    RoleUser,
		Content: "redesign this",
		Attachments: []MessageAttachment{
			{ID: "att-1", Kind: AttachmentKindImage, Filename: "image-1.png", MediaType: "image/png", SizeBytes: int64(len(imageContent)), Position: 0, Content: imageContent},
			{ID: "att-2", Kind: AttachmentKindHTML, Filename: "reference.html", MediaType: "text/html", SizeBytes: int64(len(htmlContent)), Position: 0, Content: htmlContent},
		},
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	out := string(raw)

	if strings.Contains(out, string(imageContent)) {
		t.Errorf("expected the image attachment's Content to never appear in marshaled JSON, got: %s", out)
	}
	if strings.Contains(out, string(htmlContent)) {
		t.Errorf("expected the HTML attachment's Content to never appear in marshaled JSON, got: %s", out)
	}
	if !strings.Contains(out, "image-1.png") {
		t.Errorf("expected the image attachment's filename to still be present, got: %s", out)
	}
	if !strings.Contains(out, "reference.html") {
		t.Errorf("expected the HTML attachment's filename to still be present, got: %s", out)
	}
	if !strings.Contains(out, `"size_bytes"`) {
		t.Errorf("expected size_bytes metadata to still be present, got: %s", out)
	}
}
