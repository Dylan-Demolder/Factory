package web

import (
	"io"
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
