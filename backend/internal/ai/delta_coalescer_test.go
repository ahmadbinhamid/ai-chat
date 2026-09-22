package ai

import (
	"strings"
	"testing"
)

// TestDeltaCoalescer_CoalescesManySmallChunks checks 100 tiny chunks produce far fewer
// onDelta calls, and published text exactly equals the concatenation of every chunk fed in.
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

// TestDeltaCoalescer_FlushSendsRemainder checks flush() delivers a short fragment that
// never hit either coalescing limit — otherwise a turn's tail end gets silently dropped.
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

	// A second flush with nothing buffered must be a no-op, not a spurious empty publish.
	c.flush()
	if len(published) != 1 {
		t.Fatalf("expected flushing an empty buffer to be a no-op, got %+v", published)
	}
}

// TestDeltaCoalescer_NilOnDeltaIsANoOp checks add() tolerates a nil onDelta without panicking.
func TestDeltaCoalescer_NilOnDeltaIsANoOp(t *testing.T) {
	c := newDeltaCoalescer(nil)
	c.add("anything")
	c.flush()
}
