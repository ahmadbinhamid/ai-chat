package themebuild

import (
	"fmt"
	"testing"

	"ai-chat/internal/modules/chat"
	"ai-chat/internal/urlfetch"
)

func TestLooksLikeFetchedLink(t *testing.T) {
	tests := []struct {
		filename string
		want     bool
	}{
		{"https://example.com", true},
		{"http://example.com/page", true},
		{"reference.html", false},
		{"my-export.html", false},
		{"", false},
		{"ftp://example.com", false},
	}
	for _, tt := range tests {
		if got := looksLikeFetchedLink(tt.filename); got != tt.want {
			t.Errorf("looksLikeFetchedLink(%q) = %v, want %v", tt.filename, got, tt.want)
		}
	}
}

// Regression guard: a link is capped at urlfetch.DigestHardCapBytes (16KB), not
// PostStripMaxBytes (~300KB) — comparing against the wrong cap silently always returned false.
func TestLooksTruncatedByStoredLength(t *testing.T) {
	htmlLimit := attachmentLimits[chat.AttachmentKindHTML]
	tests := []struct {
		name          string
		filename      string
		contentLength int64
		want          bool
	}{
		{
			"link at the digest hard cap is truncated",
			"https://example.com/page", urlfetch.DigestHardCapBytes, true,
		},
		{
			"link just under the digest tolerance window is not truncated",
			"https://example.com/page", urlfetch.DigestHardCapBytes - digestTruncatedLengthTolerance - 1, false,
		},
		{
			"upload at PostStripMaxBytes is truncated",
			"reference.html", htmlLimit.PostStripMaxBytes, true,
		},
		{
			"upload just under the upload tolerance window is not truncated",
			"reference.html", htmlLimit.PostStripMaxBytes - truncatedLengthTolerance - 1, false,
		},
		{
			"upload at the digest hard cap is NOT truncated (wrong cap for an upload)",
			"reference.html", urlfetch.DigestHardCapBytes, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := looksTruncatedByStoredLength(tt.filename, tt.contentLength); got != tt.want {
				t.Errorf("looksTruncatedByStoredLength(%q, %d) = %v, want %v", tt.filename, tt.contentLength, got, tt.want)
			}
		})
	}
}

func TestIsReplayedMessage(t *testing.T) {
	tests := []struct {
		name string
		m    chat.Message
		want bool
	}{
		{"user turn with content", chat.Message{Role: chat.RoleUser, Content: "hi"}, true},
		{"user turn with only whitespace content", chat.Message{Role: chat.RoleUser, Content: "   "}, false},
		{"completed assistant turn", chat.Message{Role: chat.RoleAssistant, Content: "ok", Status: chat.MessageStatusCompleted}, true},
		{"failed assistant turn", chat.Message{Role: chat.RoleAssistant, Content: "ok", Status: chat.MessageStatusFailed}, false},
		{"system turn", chat.Message{Role: chat.RoleSystem, Content: "edited a file"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReplayedMessage(tt.m); got != tt.want {
				t.Errorf("isReplayedMessage(%+v) = %v, want %v", tt.m, got, tt.want)
			}
		})
	}
}

func htmlAttachedUserMsg(id string) chat.Message {
	return chat.Message{
		ID: id, Role: chat.RoleUser, Content: "here's a link", Status: chat.MessageStatusCompleted,
		Attachments: []chat.MessageAttachment{{Kind: chat.AttachmentKindHTML, Filename: "https://example.com"}},
	}
}

func plainUserMsg(id string) chat.Message {
	return chat.Message{ID: id, Role: chat.RoleUser, Content: "plain text", Status: chat.MessageStatusCompleted}
}

func plainAssistantMsg(id string) chat.Message {
	return chat.Message{ID: id, Role: chat.RoleAssistant, Content: "ok", Status: chat.MessageStatusCompleted}
}

func TestFindCarryForwardSourceMessageID_FindsMostRecentEarlierHTMLAttachment(t *testing.T) {
	priorMessages := []chat.Message{
		htmlAttachedUserMsg("m1"),
		plainAssistantMsg("m2"),
		plainUserMsg("m3"), // current turn, no attachment
	}
	got, ok := findCarryForwardSourceMessageID(priorMessages, "m3")
	if !ok {
		t.Fatal("expected a carry-forward source to be found")
	}
	if got != "m1" {
		t.Errorf("expected source message %q, got %q", "m1", got)
	}
}

func TestFindCarryForwardSourceMessageID_PicksTheMostRecentOfSeveral(t *testing.T) {
	priorMessages := []chat.Message{
		htmlAttachedUserMsg("m1"),
		plainAssistantMsg("m2"),
		htmlAttachedUserMsg("m3"), // more recent than m1 — this one should win
		plainAssistantMsg("m4"),
		plainUserMsg("m5"), // current turn
	}
	got, ok := findCarryForwardSourceMessageID(priorMessages, "m5")
	if !ok {
		t.Fatal("expected a carry-forward source to be found")
	}
	if got != "m3" {
		t.Errorf("expected the MOST RECENT earlier attachment %q, got %q", "m3", got)
	}
}

func TestFindCarryForwardSourceMessageID_NoneFound(t *testing.T) {
	priorMessages := []chat.Message{
		plainUserMsg("m1"),
		plainAssistantMsg("m2"),
		plainUserMsg("m3"),
	}
	if _, ok := findCarryForwardSourceMessageID(priorMessages, "m3"); ok {
		t.Error("expected no carry-forward source when nothing in history ever attached HTML")
	}
}

// Confirms a message never carries forward its own attachment as an "earlier" source.
func TestFindCarryForwardSourceMessageID_ExcludesCurrentMessage(t *testing.T) {
	priorMessages := []chat.Message{
		plainUserMsg("m1"),
		htmlAttachedUserMsg("m2"), // this IS the "current" turn below
	}
	if _, ok := findCarryForwardSourceMessageID(priorMessages, "m2"); ok {
		t.Error("expected the current message's own attachment to never count as a carry-forward source")
	}
}

// Boundary: m0 stays in the window while total replayed turns <= summarizeHistoryThreshold;
// one more filler turn pushes it out. The next two tests sit on either side of that cutoff.

// Replayed count lands exactly on summarizeHistoryThreshold, so nothing is trimmed and m0 is still found.
func TestFindCarryForwardSourceMessageID_JustInsideWindow(t *testing.T) {
	priorMessages := []chat.Message{htmlAttachedUserMsg("m0")}
	for i := 0; i < summarizeHistoryThreshold-2; i++ {
		priorMessages = append(priorMessages, plainUserMsg(fmt.Sprintf("filler-%d", i)))
	}
	priorMessages = append(priorMessages, plainUserMsg("current"))

	got, ok := findCarryForwardSourceMessageID(priorMessages, "current")
	if !ok {
		t.Fatal("expected the attachment to still be inside the replayed window")
	}
	if got != "m0" {
		t.Errorf("expected source message %q, got %q", "m0", got)
	}
}

// One more filler turn than above: replayed count is threshold+1, trimming m0 off the front.
func TestFindCarryForwardSourceMessageID_JustOutsideWindow(t *testing.T) {
	priorMessages := []chat.Message{htmlAttachedUserMsg("m0")}
	for i := 0; i < summarizeHistoryThreshold-1; i++ {
		priorMessages = append(priorMessages, plainUserMsg(fmt.Sprintf("filler-%d", i)))
	}
	priorMessages = append(priorMessages, plainUserMsg("current"))

	if _, ok := findCarryForwardSourceMessageID(priorMessages, "current"); ok {
		t.Error("expected the attachment to have just fallen outside the replayed window")
	}
}

// Same cutoff with more headroom, to guard against the boundary tests above passing by coincidence.
func TestFindCarryForwardSourceMessageID_WellOutsideWindow(t *testing.T) {
	priorMessages := []chat.Message{htmlAttachedUserMsg("m0")}
	for i := 0; i < summarizeHistoryThreshold; i++ {
		priorMessages = append(priorMessages, plainUserMsg(fmt.Sprintf("filler-%d", i)))
	}
	priorMessages = append(priorMessages, plainUserMsg("current"))

	if _, ok := findCarryForwardSourceMessageID(priorMessages, "current"); ok {
		t.Error("expected the attachment to have fallen outside the replayed window and not be found")
	}
}
