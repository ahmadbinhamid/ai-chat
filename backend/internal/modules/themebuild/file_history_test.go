package themebuild

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-chat/internal/modules/chat"
)

func assistantMsg(id, content string) chat.Message {
	return chat.Message{ID: id, Role: chat.RoleAssistant, Content: content, Status: chat.MessageStatusCompleted}
}

func userMsg(id, content string) chat.Message {
	return chat.Message{ID: id, Role: chat.RoleUser, Content: content}
}

func TestFileChangesLine(t *testing.T) {
	tests := []struct {
		name    string
		changes []FileChange
		want    string
	}{
		{"no changes, no line", nil, ""},
		{"sorted, labelled by action", []FileChange{
			{FilePath: "js/minicart.js", Action: FileActionUpdate},
			{FilePath: "components/minicart.liquid", Action: FileActionUpdate},
			{FilePath: "js/new-widget.js", Action: FileActionCreate},
		}, "[Files changed this turn: components/minicart.liquid (changed), js/minicart.js (changed), js/new-widget.js (new file)]"},
		{"duplicate path listed once, create wins", []FileChange{
			{FilePath: "liquid/layout-end.liquid", Action: FileActionUpdate},
			{FilePath: "liquid/layout-end.liquid", Action: FileActionCreate},
		}, "[Files changed this turn: liquid/layout-end.liquid (new file)]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fileChangesLine(tt.changes); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToTurnsWithFileChanges_AnnotatesOnlyAssistantTurnsThatChangedFiles(t *testing.T) {
	msgs := []chat.Message{
		userMsg("u1", "the add to cart button does nothing"),
		assistantMsg("a1", "Fixed the add to cart button."),
		userMsg("u2", "what colour is the header?"),
		assistantMsg("a2", "It's dark blue."),
	}
	changes := map[string][]FileChange{
		"a1": {{FilePath: "js/minicart.js", Action: FileActionUpdate}},
		// A user message ID never gets a line, even if a row somehow matched it.
		"u2": {{FilePath: "pages/home.liquid", Action: FileActionUpdate}},
	}

	got := toTurnsWithFileChanges(msgs, changes)

	want := []string{
		"the add to cart button does nothing",
		"Fixed the add to cart button.\n\n[Files changed this turn: js/minicart.js (changed)]",
		"what colour is the header?",
		"It's dark blue.",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d turns, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i].Content != want[i] {
			t.Errorf("turn %d: got %q, want %q", i, got[i].Content, want[i])
		}
	}
	if msgs[1].Content != "Fixed the add to cart button." {
		t.Errorf("chat message content must never change, got %q", msgs[1].Content)
	}
}

func TestToTurns_UnchangedWithoutFileChanges(t *testing.T) {
	msgs := []chat.Message{userMsg("u1", "hi"), assistantMsg("a1", "Done.")}
	got := toTurns(msgs)
	if got[1].Content != "Done." {
		t.Errorf("toTurns must not annotate, got %q", got[1].Content)
	}
}

// A past turn's line must be byte-identical on every later request, or DeepSeek prefix caching breaks.
func TestToTurnsWithFileChanges_PastTurnStableAcrossRequests(t *testing.T) {
	first := []chat.Message{userMsg("u1", "make the header dark"), assistantMsg("a1", "Darkened the header.")}
	firstChanges := map[string][]FileChange{"a1": {
		{FilePath: "components/css/header.css", Action: FileActionUpdate},
		{FilePath: "components/header.liquid", Action: FileActionUpdate},
	}}
	later := append(append([]chat.Message{}, first...), userMsg("u2", "fix add to cart"), assistantMsg("a2", "Fixed it."))
	// Rows come back in a different order on the later request; the line must not depend on row order.
	laterChanges := map[string][]FileChange{
		"a1": {firstChanges["a1"][1], firstChanges["a1"][0]},
		"a2": {{FilePath: "js/minicart.js", Action: FileActionUpdate}},
	}

	a := toTurnsWithFileChanges(first, firstChanges)
	b := toTurnsWithFileChanges(later, laterChanges)
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("past turn %d changed between requests: %q vs %q", i, a[i].Content, b[i].Content)
		}
	}
}

// With file lines in history, every turn count in one 10-turn block still sends a byte-identical Summarize input.
func TestSummarizeOldTurnsCached_FileLinesStableWithinBlock(t *testing.T) {
	var msgs []chat.Message
	changes := map[string][]FileChange{}
	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			msgs = append(msgs, userMsg(fmt.Sprintf("u%d", i), fmt.Sprintf("request %d", i)))
			continue
		}
		id := fmt.Sprintf("a%d", i)
		msgs = append(msgs, assistantMsg(id, fmt.Sprintf("reply %d", i)))
		changes[id] = []FileChange{{FilePath: fmt.Sprintf("js/file-%d.js", i), Action: FileActionUpdate}}
	}

	rec := &recordingSummarizer{}
	svc := &Service{gen: rec, historySummarizationEnabled: true}
	for n := 50; n < 60; n++ {
		svc.summarizeOldTurnsCached(context.Background(), "chat-files", toTurnsWithFileChanges(msgs[:n], changes))
	}

	if len(rec.inputs) != summaryBlockSize {
		t.Fatalf("expected %d Summarize calls, got %d", summaryBlockSize, len(rec.inputs))
	}
	first := fmt.Sprintf("%v", rec.inputs[0])
	if !strings.Contains(first, "[Files changed this turn: js/file-1.js (changed)]") {
		t.Fatalf("expected file lines in the summarized turns, got %s", first)
	}
	for i, in := range rec.inputs[1:] {
		if fmt.Sprintf("%v", in) != first {
			t.Errorf("call %d in the same block sent a different Summarize input", i+1)
		}
	}
}

func TestFileChangesByChat_GroupsByMessageInOneQuery(t *testing.T) {
	conn := openTestDB(t)
	chatSvc := chat.NewService(chat.NewRepository(conn))
	buildRepo := NewRepository(conn)
	ctx := context.Background()

	c, err := chatSvc.GetOrCreateChat(ctx, uint64(time.Now().UnixNano()), ChatType)
	if err != nil {
		t.Fatalf("GetOrCreateChat failed: %v", err)
	}
	m1 := seedPendingFile(t, chatSvc, buildRepo, c, "pages/home.liquid", "v1", GeneratedFileKindProposed)
	m2 := seedPendingFile(t, chatSvc, buildRepo, c, "js/minicart.js", "v1", GeneratedFileKindProposed)

	got, err := buildRepo.FileChangesByChat(ctx, c.ID)
	if err != nil {
		t.Fatalf("FileChangesByChat failed: %v", err)
	}
	if len(got) != 2 || got[m1.ID][0].FilePath != "pages/home.liquid" || got[m2.ID][0].FilePath != "js/minicart.js" {
		t.Errorf("unexpected grouping: %+v", got)
	}
	if got[m1.ID][0].Action != FileActionCreate {
		t.Errorf("expected persisted action create, got %q", got[m1.ID][0].Action)
	}
}
