package tui

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// waitForWaiting polls until Ask is blocking — Ask runs on the interview's
// goroutine, so the test has to meet it there.
func waitForWaiting(t *testing.T, a *Asker) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, waiting, _ := a.Snapshot(); waiting {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("Ask never started waiting for an answer")
}

func TestAskBlocksUntilAnswered(t *testing.T) {
	// The notify callback fires on whichever goroutine mutated the
	// transcript — Ask's goroutine and the answering goroutine both do —
	// so the counter has to be safe to touch from all of them.
	var notified atomic.Int32
	a := NewAsker(func() { notified.Add(1) })

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		text, err := a.Ask("What should it do?", "y", "/done")
		done <- result{text, err}
	}()
	waitForWaiting(t, a)

	lines, waiting, quick := a.Snapshot()
	if !waiting {
		t.Fatal("not waiting while Ask is blocked")
	}
	if len(lines) == 0 || lines[len(lines)-1].Text != "What should it do?" {
		t.Fatalf("question not in transcript: %+v", lines)
	}
	if len(quick) != 2 {
		t.Errorf("quick = %v, want 2 shortcuts", quick)
	}

	if err := a.Answer("search across notes"); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Ask returned %v", got.err)
		}
		if got.text != "search across notes" {
			t.Errorf("answer = %q", got.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask did not release after Answer")
	}
	if notified.Load() == 0 {
		t.Error("notify callback never fired — the UI would never redraw")
	}
}

func TestAnswerWithNothingWaiting(t *testing.T) {
	a := NewAsker(nil)
	if err := a.Answer("hello"); err != ErrNotWaiting {
		t.Errorf("Answer with no question = %v, want ErrNotWaiting", err)
	}
}

// Leaving a project mid-interview must release a blocked Ask rather than
// leak the goroutine forever.
func TestCloseReleasesBlockedAsk(t *testing.T) {
	a := NewAsker(nil)
	done := make(chan error, 1)
	go func() {
		_, err := a.Ask("still waiting?")
		done <- err
	}()
	waitForWaiting(t, a)
	a.Close()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Ask after Close = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not release Ask — goroutine leaked")
	}
	// A second Close must not panic (sync.Once).
	a.Close()
}

func TestTranscriptRolesAndWriter(t *testing.T) {
	a := NewAsker(nil)
	a.System("interview started")
	a.Error("the build failed")
	a.Say("Drafting the spec")
	if n, err := a.Write([]byte("engine log line\n")); err != nil || n == 0 {
		t.Fatalf("Write = %d, %v", n, err)
	}

	lines, _, _ := a.Snapshot()
	want := []string{"system", "error", "agent", "log"}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %+v", len(lines), len(want), lines)
	}
	for i, role := range want {
		if lines[i].Role != role {
			t.Errorf("line %d role = %q, want %q", i, lines[i].Role, role)
		}
	}
	if lines[3].Text != "engine log line" {
		t.Errorf("writer line = %q", lines[3].Text)
	}
	// The transcript handed back to the renderer must be a copy: the
	// interview goroutine keeps appending to the original.
	lines[0].Role = "mutated"
	again, _, _ := a.Snapshot()
	if again[0].Role != "system" {
		t.Error("Snapshot aliased internal state — concurrent append would corrupt the view")
	}
}

func TestSayIgnoresBlankMessages(t *testing.T) {
	a := NewAsker(nil)
	a.Say("   \n ")
	lines, _, _ := a.Snapshot()
	if len(lines) != 0 {
		t.Errorf("blank Say added a line: %+v", lines)
	}
}

// Regression: an answer handed over before Ask reached its select used to be
// dropped by Answer's default branch — ErrNotWaiting for the user, and an
// Ask that never returned.
//
// Ask sets waiting, unlocks, fires a redraw, and only then blocks. Answer
// landing in that gap passed the waiting check but found no receiver. It
// showed up as an intermittent TestAskBlocksUntilAnswered failure and, in
// the running program, as an interview that silently ignored what you typed.
// The answers channel is buffered now, so the reply waits for Ask instead.
func TestAnswerNeverDroppedBetweenAskAndItsSelect(t *testing.T) {
	type res struct {
		text string
		err  error
	}
	for i := 0; i < 500; i++ {
		a := NewAsker(nil)
		done := make(chan res, 1)
		go func() {
			text, err := a.Ask("What should it do?")
			done <- res{text, err}
		}()
		// Spin, don't sleep: land in the window while it is still open.
		for {
			if _, waiting, _ := a.Snapshot(); waiting {
				break
			}
		}
		if err := a.Answer("the answer"); err != nil {
			t.Fatalf("iteration %d: Answer dropped the reply: %v", i, err)
		}
		select {
		case got := <-done:
			if got.err != nil {
				t.Fatalf("iteration %d: Ask returned %v", i, got.err)
			}
			if got.text != "the answer" {
				t.Fatalf("iteration %d: Ask got %q, want %q", i, got.text, "the answer")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: Ask never returned — the answer was lost", i)
		}
	}
}

// Close must leave waiting false, or later answers look accepted while
// nobody is left to read them.
func TestWaitingClearsWhenAskEnds(t *testing.T) {
	a := NewAsker(nil)
	done := make(chan error, 1)
	go func() {
		_, err := a.Ask("still open?")
		done <- err
	}()
	waitForWaiting(t, a)
	a.Close()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Ask after Close = %v, want context.Canceled", err)
	}
	if _, waiting, _ := a.Snapshot(); waiting {
		t.Error("waiting is still true after Ask ended — the next answer would be swallowed")
	}
}
