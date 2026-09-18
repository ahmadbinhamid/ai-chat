package genlifecycle_test

import (
	"sync"
	"testing"

	"ai-chat/internal/genlifecycle"
)

func TestIsTerminal(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"done", true},
		{"failed", true},
		{"cancelled", true},
		{"started", false},
		{"thinking", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := genlifecycle.IsTerminal(tc.typ); got != tc.want {
			t.Errorf("IsTerminal(%q)=%v want %v", tc.typ, got, tc.want)
		}
	}
}

func TestTerminalGuard_FirstWins(t *testing.T) {
	var g genlifecycle.TerminalGuard
	if !g.TryClaimTerminal(genlifecycle.EventDone) {
		t.Fatal("first claim should succeed")
	}
	if g.TryClaimTerminal(genlifecycle.EventFailed) {
		t.Fatal("second terminal must be suppressed")
	}
	if g.TryClaimTerminal(genlifecycle.EventCancelled) {
		t.Fatal("third terminal must be suppressed")
	}
	if g.Kind() != genlifecycle.EventDone {
		t.Fatalf("kind=%q want done", g.Kind())
	}
	if g.ShouldEmitNonTerminal() {
		t.Fatal("non-terminal emit must be rejected after terminal")
	}
}

func TestTerminalGuard_CancelWinsOverLateProgress(t *testing.T) {
	var g genlifecycle.TerminalGuard
	if !g.TryClaimTerminal(genlifecycle.EventCancelled) {
		t.Fatal("cancel claim failed")
	}
	if g.ShouldEmitNonTerminal() {
		t.Fatal("progress after cancel must not emit")
	}
	if g.TryClaimTerminal(genlifecycle.EventDone) {
		t.Fatal("done after cancel must not claim")
	}
}

func TestTerminalGuard_ConcurrentClaims(t *testing.T) {
	var g genlifecycle.TerminalGuard
	types := []string{genlifecycle.EventDone, genlifecycle.EventFailed, genlifecycle.EventCancelled}
	var wins sync.Map
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			typ := types[i%len(types)]
			if g.TryClaimTerminal(typ) {
				wins.Store(typ, true)
			}
		}(i)
	}
	wg.Wait()
	count := 0
	wins.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 1 {
		t.Fatalf("expected exactly one winning terminal type, got %d", count)
	}
	if !g.Claimed() {
		t.Fatal("guard should be claimed")
	}
}

func TestTerminalGuard_RaceDoneVsFailedVsCancelled(t *testing.T) {
	// Stress the three terminal races the Phase 6 brief requires.
	for _, pair := range [][2]string{
		{genlifecycle.EventDone, genlifecycle.EventFailed},
		{genlifecycle.EventDone, genlifecycle.EventCancelled},
		{genlifecycle.EventFailed, genlifecycle.EventCancelled},
	} {
		var g genlifecycle.TerminalGuard
		var wg sync.WaitGroup
		var aOK, bOK bool
		wg.Add(2)
		go func() {
			defer wg.Done()
			aOK = g.TryClaimTerminal(pair[0])
		}()
		go func() {
			defer wg.Done()
			bOK = g.TryClaimTerminal(pair[1])
		}()
		wg.Wait()
		if aOK == bOK {
			t.Fatalf("pair %v: expected exactly one winner, aOK=%v bOK=%v", pair, aOK, bOK)
		}
		if g.ShouldEmitNonTerminal() {
			t.Fatalf("pair %v: progress must be blocked after terminal", pair)
		}
	}
}
