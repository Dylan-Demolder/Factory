package config

import (
	"strings"
	"testing"
	"time"
)

// The shipped config is deliberately empty: factory has no opinion about
// whose models you use. It must still load, carry the documented defaults,
// and report itself as needing setup.
func TestExampleParses(t *testing.T) {
	c, err := Parse([]byte(ExampleJSON), "/opt/factory")
	if err != nil {
		t.Fatal(err)
	}
	if c.Configured() {
		t.Error("the shipped config came with agents — factory must not pick any for the user")
	}
	if len(c.Agents) != 0 || c.Roles.Builder != "" {
		t.Errorf("shipped config is not empty: agents=%v builder=%q", c.Agents, c.Roles.Builder)
	}
	if len(c.Roundtable.Participants) != 0 {
		t.Errorf("shipped config seats a panel: %v", c.Roundtable.Participants)
	}
	if !c.Roundtable.TasksEnabled() {
		t.Error("roundtables should default to on")
	}
	if c.Limits.AgentTimeout.Duration != 30*time.Minute {
		t.Fatalf("agent timeout = %s", c.Limits.AgentTimeout)
	}
	if c.Limits.MaxTaskAttempts != 4 || c.Limits.MaxInterviewQuestions != 5 {
		t.Errorf("limits not defaulted: %+v", c.Limits)
	}
}

func TestDefaultsRolesToOpencode(t *testing.T) {
	c, err := Parse([]byte(`{"agents":{"oc":{"type":"opencode"}}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Roles.Interviewer != "oc" || c.Roles.Reviewer != "oc" || c.Roles.Builder != "oc" {
		t.Fatalf("roles not defaulted: %+v", c.Roles)
	}
	if c.Limits.MaxTaskAttempts != 4 || c.Roundtable.Rounds != 2 {
		t.Fatalf("limits not defaulted: %+v", c.Limits)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]string{
		// An agent defined without a command: partially configured, still wrong.
		`{"agents":{"a":{"type":"command"}}}`: "command is required",
		`{"agents":{"a":{"type":"banana"}},"roles":{"builder":"a","interviewer":"a","planner":"a","reviewer":"a","moderator":"a"}}`: "unknown type",
		`{"agents":{"a":{"type":"opencode"}},"roles":{"reviewer":"ghost"}}`:                                                         "unknown agent",
		`{"agents":{"a":{"type":"openai","base_url":"http://x","model":"m"}},"roles":{"builder":"a"}}`:                              "cannot edit files",
		`{"agents":{"a":{"type":"opencode"}},"typo":1}`:                                                                             "unknown field",
		// Seated before anybody exists to sit in it.
		`{"agents":{},"roles":{"builder":"ghost"}}`: "unknown agent",
		// A model pinned to a seat that has no agent to run it.
		`{"agents":{},"role_models":{"builder":"some-model"}}`: "no agents are configured yet",
	}
	for in, want := range cases {
		_, err := Parse([]byte(in), "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want error containing %q", in, err, want)
		}
	}
}

// `factory init` writes a config with no agents at all: factory ships no
// opinion about whose models to use. It must load, and RequireConfigured
// must say how to fix it rather than leaving the user with a bare error.
func TestEmptyConfigIsValidButUnconfigured(t *testing.T) {
	for _, in := range []string{
		`{"agents":{}}`,
		`{"agents":{},"roles":{}}`,
		`{"agents":{},"roundtable":{"participants":[]}}`,
	} {
		c, err := Parse([]byte(in), "")
		if err != nil {
			t.Errorf("%s: %v — an empty config must load", in, err)
			continue
		}
		if c.Configured() {
			t.Errorf("%s: Configured() = true, want false", in)
		}
		if err := c.RequireConfigured(); err == nil {
			t.Errorf("%s: RequireConfigured() = nil, want guidance", in)
		} else if !strings.Contains(err.Error(), "factory agent add") {
			t.Errorf("%s: guidance = %q, want it to name the command to run", in, err)
		}
	}
	// With agents present, RequireConfigured steps aside.
	c, err := Parse([]byte(`{"agents":{"a":{"type":"opencode"}}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Configured() || c.RequireConfigured() != nil {
		t.Errorf("a config with an agent should be configured: Configured=%v err=%v", c.Configured(), c.RequireConfigured())
	}
}
