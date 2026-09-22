package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/state"
)

// The whole point of role_models is that a seat can run a different model
// from the agent that fills it. The fake here is a real command agent that
// prints the model it was handed, so the assertion is on what the subprocess
// actually received — not on config plumbing.
func TestRoleModelOverrideReachesTheAgent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-model.sh")
	// args are: --model <model>
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'model=%s\\n' \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgJSON := fmt.Sprintf(`{
  "agents": {"fake": {"type":"command","command":%q,"args":["--model","{{model}}"],"model":"base-model"}},
  "roles": {"interviewer":"fake","planner":"fake","builder":"fake","reviewer":"fake","moderator":"fake"},
  "role_models": {"reviewer":"opencode-go/kimi-k3"}
}`, script)

	cfg, err := config.Parse([]byte(cfgJSON), dir)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	agents, err := agent.NewAll(cfg)
	if err != nil {
		t.Fatalf("agents: %v", err)
	}
	e := New(cfg, agents, &state.Store{Root: dir}, &state.Project{}, io.Discard)
	ctx := context.Background()

	// The overridden seat must receive the override, not the agent's model.
	out, err := e.callRole(ctx, config.RoleReviewer, agent.Request{Stage: "t", Prompt: "hi"})
	if err != nil {
		t.Fatalf("reviewer call: %v", err)
	}
	if !strings.Contains(out, "model=opencode-go/kimi-k3") {
		t.Errorf("reviewer got %q, want the role_models override", strings.TrimSpace(out))
	}

	// A seat with no override keeps the agent's own model.
	out, err = e.callRole(ctx, config.RoleBuilder, agent.Request{Stage: "t", Prompt: "hi"})
	if err != nil {
		t.Fatalf("builder call: %v", err)
	}
	if !strings.Contains(out, "model=base-model") {
		t.Errorf("builder got %q, want the agent's own model", strings.TrimSpace(out))
	}

	// Logs and events should show which model a seat is on, so a run that
	// looks wrong can be traced to its configuration.
	if got := e.roleLabel(config.RoleReviewer); got != "fake (opencode-go/kimi-k3)" {
		t.Errorf("reviewer label = %q", got)
	}
	if got := e.roleLabel(config.RoleBuilder); got != "fake" {
		t.Errorf("builder label = %q", got)
	}
}

// Roundtable's moderator argument is a role, so the synthesis must go through
// the same override path as every other seat.
func TestRoundtableModeratorUsesRoleModel(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echo-model.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '```json\n{\"ok\":true}\n```'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgJSON := fmt.Sprintf(`{
  "agents": {"fake": {"type":"command","command":%q,"args":["{{model}}","{{prompt}}"]}},
  "roles": {"interviewer":"fake","planner":"fake","builder":"fake","reviewer":"fake","moderator":"fake"},
  "roundtable": {"rounds":1,"participants":[]},
  "role_models": {"moderator":"some-frontier-model"}
}`, script)
	cfg, err := config.Parse([]byte(cfgJSON), dir)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	agents, err := agent.NewAll(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := New(cfg, agents, &state.Store{Root: dir}, &state.Project{}, io.Discard)

	if got := e.roleLabel(config.RoleModerator); got != "fake (some-frontier-model)" {
		t.Errorf("moderator label = %q — Roundtable would synthesise with the wrong model", got)
	}
	if _, err := e.Roundtable(context.Background(), "t", "topic", "material", config.RoleModerator, "say done"); err != nil {
		t.Fatalf("roundtable: %v", err)
	}
}

// An empty seat must fail loudly rather than fall back to some other agent.
//
// config.Parse can never produce this state — it rejects a config with an
// unassigned role, and applyDefaults fills seats from the first opencode
// agent — so the Config is built directly to reach the defensive branch.
// (Parsing `{"agents":{"a":{"type":"opencode"}}}` would silently fill every
// seat with "a", and callRole would then launch the real opencode binary.)
func TestSeatWithNoAgentAssigned(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Agents: map[string]config.Agent{"a": {Type: "command", Command: "/bin/true"}},
		Roles:  config.Roles{}, // every seat deliberately empty
	}
	agents, _ := agent.NewAll(cfg)
	e := New(cfg, agents, &state.Store{Root: dir}, &state.Project{}, io.Discard)

	if _, err := e.callRole(context.Background(), config.RoleBuilder, agent.Request{Stage: "t"}); err == nil {
		t.Fatal("expected an error for an unassigned seat")
	} else if !strings.Contains(err.Error(), "no agent assigned") {
		t.Errorf("err = %v", err)
	}
	if got := e.roleLabel(config.RoleBuilder); got != "builder" {
		t.Errorf("roleLabel for an empty seat = %q, want the role name", got)
	}
}
