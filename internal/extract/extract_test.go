package extract

import "testing"

type reply struct {
	Verdict string   `json:"verdict"`
	Issues  []string `json:"issues"`
}

func TestJSONPrefersLastJSONFence(t *testing.T) {
	text := "thinking...\n```json\n{\"verdict\":\"fail\"}\n```\nactually:\n```json\n{\"verdict\":\"pass\",\"issues\":[]}\n```\n"
	var r reply
	if err := JSON(text, &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "pass" {
		t.Fatalf("got %q, want pass", r.Verdict)
	}
}

func TestJSONBareObjectWithBracesInStrings(t *testing.T) {
	text := `Sure! Here you go: {"verdict":"fail","issues":["missing } brace handling", "a \"quoted\" {thing}"]} hope that helps`
	var r reply
	if err := JSON(text, &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "fail" || len(r.Issues) != 2 || r.Issues[1] != `a "quoted" {thing}` {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestJSONSkipsInvalidCandidate(t *testing.T) {
	text := "```json\n{not json}\n```\n{\"verdict\":\"pass\"}"
	var r reply
	if err := JSON(text, &r); err != nil {
		t.Fatal(err)
	}
	if r.Verdict != "pass" {
		t.Fatalf("got %q", r.Verdict)
	}
}

func TestJSONNone(t *testing.T) {
	var r reply
	if err := JSON("no structured data here", &r); err == nil {
		t.Fatal("expected error")
	}
}

func TestBeforeLastJSONFence(t *testing.T) {
	text := "# Spec\n\nBody with ```go\ncode\n```\n\n```json\n{\"a\":1}\n```"
	got := BeforeLastJSONFence(text)
	want := "# Spec\n\nBody with ```go\ncode\n```"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if BeforeLastJSONFence("plain") != "plain" {
		t.Fatal("plain text should be returned unchanged")
	}
}
