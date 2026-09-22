package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dylan-demolder/factory/internal/config"
)

func TestCommandStdin(t *testing.T) {
	a := newCommand("echo", config.Agent{Command: "sh", Args: []string{"-c", "tr a-z A-Z"}}, 5*time.Second)
	out, err := a.Run(context.Background(), Request{System: "sys", Prompt: "hello", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "SYS") || !strings.Contains(out, "HELLO") {
		t.Fatalf("got %q", out)
	}
}

func TestCommandPlaceholders(t *testing.T) {
	a := newCommand("x", config.Agent{
		Command:      "sh",
		Model:        "m1",
		Args:         []string{"-c", `printf '%s|%s|' "$1" "$2"; cat "$3"`, "_", "{{model}}", "{{prompt}}", "{{prompt_file}}"},
		ReadOnlyArgs: []string{"--ro"},
	}, 5*time.Second)
	out, err := a.Run(context.Background(), Request{Prompt: "hi", Dir: t.TempDir(), ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if out != "m1|hi|hi" {
		t.Fatalf("got %q", out)
	}
}

func TestCommandTimeoutKillsGroup(t *testing.T) {
	a := newCommand("slow", config.Agent{Command: "sh", Args: []string{"-c", "sleep 30 & wait"}}, 300*time.Millisecond)
	start := time.Now()
	_, err := a.Run(context.Background(), Request{Prompt: "x", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout did not kill the process tree")
	}
}

func TestCommandFailureIncludesStderr(t *testing.T) {
	a := newCommand("bad", config.Agent{Command: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}}, 5*time.Second)
	_, err := a.Run(context.Background(), Request{Prompt: "x", Dir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestOpencodeArgs(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "opencode")
	// Fake opencode: print argv one per line.
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nfor a in \"$@\"; do echo \"[$a]\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := newOpencode("oc", config.Agent{Command: fake, Model: "prov/model"}, 5*time.Second)

	out, err := a.Run(context.Background(), Request{Prompt: "do it", Dir: dir, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	want := "[run]\n[--model]\n[prov/model]\n[--agent]\n[plan]\n[do it]"
	if out != want {
		t.Fatalf("got\n%s\nwant\n%s", out, want)
	}

	big := strings.Repeat("x", maxArgPrompt+1)
	out, err = a.Run(context.Background(), Request{Prompt: big, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[plan]") || !strings.Contains(out, ".factory/tmp/prompt-") {
		t.Fatalf("large prompt not moved to file: %.200s", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".factory", "tmp")); len(entries) != 0 {
		t.Fatal("prompt file not cleaned up")
	}
}

func TestOpenAI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sekret" {
			http.Error(w, "bad request", 400)
			return
		}
		var body struct {
			Model    string        `json:"model"`
			Messages []chatMessage `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		reply := body.Model + ":" + body.Messages[0].Role + ":" + body.Messages[1].Content
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": reply}}}})
	}))
	defer srv.Close()
	t.Setenv("TEST_KEY", "sekret")
	a := newOpenAI("o", config.Agent{BaseURL: srv.URL + "/v1/", Model: "gpt", APIKeyEnv: "TEST_KEY"}, 5*time.Second)
	out, err := a.Run(context.Background(), Request{System: "s", Prompt: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "gpt:system:p" {
		t.Fatalf("got %q", out)
	}
}
