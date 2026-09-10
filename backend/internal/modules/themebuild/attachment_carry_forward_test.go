package themebuild

import (
	"fmt"
	"testing"

	"ai-chat/internal/modules/chat"
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

// TestFindCarryForwardSourceMessageID_ExcludesCurrentMessage confirms a
// message never carries forward its own attachment — doGenerate only ever
// calls this once it already knows the current turn resolved nothing, so in
// practice the current message never has an attachment either, but the
// exclusion is asserted directly here regardless: if the current message
// somehow does carry one, it must still not be treated as a valid "earlier"
// source for itself.
func TestFindCarryForwardSourceMessageID_ExcludesCurrentMessage(t *testing.T) {
	priorMessages := []chat.Message{
		plainUserMsg("m1"),
		htmlAttachedUserMsg("m2"), // this IS the "current" turn below
	}
	if _, ok := findCarryForwardSourceMessageID(priorMessages, "m2"); ok {
		t.Error("expected the current message's own attachment to never count as a carry-forward source")
	}
}

// The window findCarryForwardSourceMessageID applies mirrors exactly what
// summarizeOldTurns/summarizeOldTurnsCached keep verbatim: the most recent
// summarizeHistoryThreshold replayed turns, `turns[len(turns)-threshold:]`.
// m0 (the attachment-carrying turn) is itself one slot in that window, so
// it stays inside as long as the TOTAL replayed-turn count (m0 + fillers +
// the current turn) is at most summarizeHistoryThreshold — i.e. at most
// summarizeHistoryThreshold-2 filler turns in between. One more filler than
// that pushes the total to threshold+1, and index(m0)=0 falls below the new
// windowStart of 1, dropping it out. These two tests sit right either side
// of that exact boundary.

// TestFindCarryForwardSourceMessageID_JustInsideWindow: threshold-2 filler
// turns between m0 and the current turn — total replayed count is exactly
// summarizeHistoryThreshold, so nothing is trimmed and m0 is still found.
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

// TestFindCarryForwardSourceMessageID_JustOutsideWindow: one filler turn
// more than the case above — total replayed count is threshold+1, which
// trims exactly one turn off the front (m0) and m0 must no longer be found.
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

// TestFindCarryForwardSourceMessageID_WellOutsideWindow is the same cutoff
// with more headroom (threshold filler turns, not just threshold-1) —
// guards against the boundary tests above passing only by coincidence.
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
