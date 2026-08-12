package ai

import (
	"strings"
	"testing"
)

// TestDeltaCoalescer_CoalescesManySmallChunks is item 6: 100 tiny chunks
// must produce far fewer onDelta calls than 100, and the concatenation of
// whatever WAS published must exactly equal the concatenation of every
// chunk fed in — coalescing must never drop or reorder text, only batch it.
func TestDeltaCoalescer_CoalescesManySmallChunks(t *testing.T) {
	var published []string
	c := newDeltaCoalescer(func(s string) { published = append(published, s) })

	var want strings.Builder
	for i := 0; i < 100; i++ {
		chunk := "x"
		want.WriteString(chunk)
		c.add(chunk)
	}
	c.flush()

	if len(published) >= 100 {
		t.Fatalf("expected coalescing to reduce 100 one-character chunks to far fewer onDelta calls, got %d", len(published))
	}
	if len(published) == 0 {
		t.Fatal("expected at least one onDelta call")
	}

	var got strings.Builder
	for _, p := range published {
		got.WriteString(p)
	}
	if got.String() != want.String() {
		t.Fatalf("expected the concatenated published text to equal the concatenated input;\nwant %q\ngot  %q", want.String(), got.String())
	}
}

// TestDeltaCoalescer_FlushSendsRemainder confirms flush() delivers a short
// fragment that never hit either coalescing limit on its own — without
// this, the tail end of a turn's narration (very often exactly this case:
// the stream just ended, so nothing else ever triggers another add() call)
// would be silently dropped.
func TestDeltaCoalescer_FlushSendsRemainder(t *testing.T) {
	var published []string
	c := newDeltaCoalescer(func(s string) { published = append(published, s) })

	c.add("short")
	if len(published) != 0 {
		t.Fatalf("expected nothing published yet (under both limits), got %+v", published)
	}
	c.flush()
	if len(published) != 1 || published[0] != "short" {
		t.Fatalf("expected flush to deliver the buffered remainder, got %+v", published)
	}

	// A second flush with nothing buffered must be a no-op, not a spurious
	// empty-string publish.
	c.flush()
	if len(published) != 1 {
		t.Fatalf("expected flushing an empty buffer to be a no-op, got %+v", published)
	}
}

// TestDeltaCoalescer_NilOnDeltaIsANoOp mirrors Generate's own contract:
// onDelta is optional (most callers, e.g. Summarize, never pass one), so
// add() must tolerate a nil onDelta without panicking.
func TestDeltaCoalescer_NilOnDeltaIsANoOp(t *testing.T) {
	c := newDeltaCoalescer(nil)
	c.add("anything")
	c.flush()
}
