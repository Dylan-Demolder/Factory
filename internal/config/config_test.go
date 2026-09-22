package config

import (
	"strings"
	"testing"
	"time"
)

func TestExampleParses(t *testing.T) {
	c, err := Parse([]byte(ExampleJSON), "/opt/factory")
	if err != nil {
		t.Fatal(err)
	}
	if c.Roles.Builder != "opencode" {
		t.Fatalf("builder = %q", c.Roles.Builder)
	}
	if got := c.Agents["camel-a"].Args[0]; got != "/opt/factory/adapters/camel_agent.py" {
		t.Fatalf("config_dir not expanded: %q", got)
	}
	if c.Limits.AgentTimeout.Duration != 30*time.Minute {
		t.Fatalf("agent timeout = %s", c.Limits.AgentTimeout)
	}
	if len(c.Roundtable.Participants) != 3 || !c.Roundtable.TasksEnabled() {
		t.Fatal("roundtable not loaded")
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
		`{"agents":{}}`: "no agents",
		`{"agents":{"a":{"type":"banana"}},"roles":{"builder":"a","interviewer":"a","planner":"a","reviewer":"a","moderator":"a"}}`: "unknown type",
		`{"agents":{"a":{"type":"opencode"}},"roles":{"reviewer":"ghost"}}`:                                                         "unknown agent",
		`{"agents":{"a":{"type":"openai","base_url":"http://x","model":"m"}},"roles":{"builder":"a"}}`:                              "cannot edit files",
		`{"agents":{"a":{"type":"opencode"}},"typo":1}`:                                                                             "unknown field",
	}
	for in, want := range cases {
		_, err := Parse([]byte(in), "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want error containing %q", in, err, want)
		}
	}
}
