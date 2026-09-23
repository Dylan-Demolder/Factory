package web

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestChatAskAnswer(t *testing.T) {
	c := NewChat()
	if err := c.Answer("early"); err != ErrNotWaiting {
		t.Fatalf("want ErrNotWaiting, got %v", err)
	}
	got := make(chan string)
	go func() {
		a, _ := c.Ask("Approve?", "y", "yes")
		got <- a
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, waiting := c.Since(0); waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
	msgs, _ := c.Since(0)
	if len(msgs[0].Quick) != 1 || msgs[0].Quick[0].Value != "y" {
		t.Fatalf("quick replies: %+v", msgs[0].Quick)
	}
	if err := c.Answer("y"); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a != "y" {
		t.Fatalf("got %q", a)
	}
	msgs, waiting := c.Since(1)
	if waiting || len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("after answer: %+v %v", msgs, waiting)
	}
}

func TestChatCloseUnblocksAsk(t *testing.T) {
	c := NewChat()
	done := make(chan error)
	go func() {
		_, err := c.Ask("q")
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	c.Close() // idempotent
	if err := <-done; err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestChatWriterAndSince(t *testing.T) {
	c := NewChat()
	c.Write([]byte("[12:00:00] one\n\n[12:00:01] two\n"))
	c.Say("  ")
	msgs, _ := c.Since(-5)
	if len(msgs) != 2 || msgs[1].Text != "[12:00:01] two" || msgs[0].Role != "system" {
		t.Fatalf("%+v", msgs)
	}
	if m, _ := c.Since(99); len(m) != 0 {
		t.Fatal("since beyond end should be empty")
	}
}

// A round posted as a batch must need exactly one submission of N answers —
// that is what turns eight question turns into one.
func TestChatAskManyNeedsOneSubmission(t *testing.T) {
	c := NewChat()
	done := make(chan []string, 1)
	go func() {
		a, _ := c.AskMany([]string{"Who is it for?", "What stack?", "Success?"}, "/done")
		done <- a
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		msgs, waiting := c.Since(0)
		if waiting && len(msgs) == 3 && c.Pending() == 3 {
			if !strings.HasPrefix(msgs[0].Text, "[1/3]") {
				t.Fatalf("questions should be numbered for the form: %q", msgs[0].Text)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never batched: waiting=%v msgs=%d pending=%d", waiting, len(msgs), c.Pending())
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A partial submission is refused, naming the requirement.
	if err := c.AnswerBatch([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("partial batch accepted: %v", err)
	}
	// So is the single-answer path while a batch is pending — otherwise the
	// browser would silently answer one of eight.
	if err := c.Answer("one only"); err == nil || !strings.Contains(err.Error(), "answers") {
		t.Fatalf("single answer during a batch accepted: %v", err)
	}

	want := []string{"Accountants", "", "/done"}
	if err := c.AnswerBatch(want); err != nil {
		t.Fatalf("batch rejected: %v", err)
	}
	got := <-done
	if len(got) != len(want) {
		t.Fatalf("engine got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("answer %d = %q, want %q", i, got[i], want[i])
		}
	}
	if c.Pending() != 0 {
		t.Errorf("pending left at %d after the batch", c.Pending())
	}
	// The transcript records every answer, blanks shown as no-preference.
	msgs, waiting := c.Since(0)
	if waiting || len(msgs) != 6 {
		t.Fatalf("after batch: waiting=%v msgs=%d", waiting, len(msgs))
	}
	if msgs[4].Text != "[2/3] (no preference)" {
		t.Errorf("blank recorded as %q", msgs[4].Text)
	}
}

// A one-question round still accepts the plain {"text": …} path, so existing
// curl examples keep working.
func TestChatSinglePendingStillTakesText(t *testing.T) {
	c := NewChat()
	done := make(chan []string, 1)
	go func() {
		a, _ := c.AskMany([]string{"Approve the spec?"}, "y")
		done <- a
	}()
	deadline := time.Now().Add(2 * time.Second)
	for c.Pending() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("never pending")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.Answer("y"); err != nil {
		t.Fatalf("single answer rejected: %v", err)
	}
	if got := <-done; len(got) != 1 || got[0] != "y" {
		t.Fatalf("got %q", got)
	}
}
