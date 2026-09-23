package ui

import (
	"io"
	"strings"
	"testing"
)

func TestAskMultiline(t *testing.T) {
	p := New(strings.NewReader("line one\nline two\n\nnext\n\n\n"), io.Discard)
	a, err := p.Ask("q1")
	if err != nil || a != "line one\nline two" {
		t.Fatalf("got %q %v", a, err)
	}
	a, _ = p.Ask("q2")
	if a != "next" {
		t.Fatalf("got %q", a)
	}
	a, err = p.Ask("q3")
	if err != nil || a != "" {
		t.Fatalf("empty answer: got %q %v", a, err)
	}
	_, err = p.Ask("q4")
	if err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestConfirm(t *testing.T) {
	p := New(strings.NewReader("\nn\nYes\n"), io.Discard)
	if !p.Confirm("a", true) || p.Confirm("b", true) || !p.Confirm("c", false) {
		t.Fatal("confirm mismatch")
	}
	if p.Confirm("eof", true) {
		t.Fatal("EOF must not confirm")
	}
}

func TestAskQuick(t *testing.T) {
	p := New(strings.NewReader("Y\nmore\nlines\n\n"), io.Discard)
	a, err := p.Ask("approve?", "y", "yes")
	if err != nil || a != "Y" {
		t.Fatalf("got %q %v", a, err)
	}
	a, _ = p.Ask("approve?", "y")
	if a != "more\nlines" {
		t.Fatalf("got %q", a)
	}
}

// The whole point of the batch: one block of input answers the round.
func TestAskManyReadsOneLinePerQuestion(t *testing.T) {
	p := New(strings.NewReader("Busy developers\n\nthird answer\n/done\n"), io.Discard)
	got, err := p.AskMany([]string{"q1", "q2", "q3", "q4"}, "/done")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := []string{"Busy developers", "", "third answer", "/done"}
	if len(got) != len(want) {
		t.Fatalf("got %d answers %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("answer %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A short stdin must not hang: whatever was read comes back, marked EOF.
func TestAskManyStopsOnEOF(t *testing.T) {
	p := New(strings.NewReader("only this\n"), io.Discard)
	got, err := p.AskMany([]string{"q1", "q2", "q3"})
	if err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
	if len(got) != 3 || got[0] != "only this" {
		t.Fatalf("got %q", got)
	}
	if got[1] != "" || got[2] != "" {
		t.Errorf("unread slots should be empty: %q", got)
	}
}

func TestAskManyWithNoQuestions(t *testing.T) {
	p := New(strings.NewReader(""), io.Discard)
	got, err := p.AskMany(nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v — want nothing at all", got, err)
	}
}
