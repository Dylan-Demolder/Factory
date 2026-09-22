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
