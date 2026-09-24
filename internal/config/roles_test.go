package config

import (
	"strings"
	"testing"
)

const roleModelsBase = `{
  "agents": {
    "oc":   {"type": "opencode", "model": "opencode-go/glm-5.3"},
    "cli":  {"type": "command", "command": "claude", "args": ["-p", "--model", "{{model}}"]},
    "bare": {"type": "command", "command": "curl", "args": ["-s"]}
  },
  "roles": {"interviewer":"oc","planner":"oc","builder":"oc","reviewer":"cli","moderator":"bare"}
}`

func TestRoleModelsAccepted(t *testing.T) {
	// The base config has to parse before we vary it — otherwise a later
	// failure could be blamed on the override rather than the fixture.
	if _, err := Parse([]byte(roleModelsBase), ""); err != nil {
		t.Fatal(err)
	}
	// Adding an override must not disturb anything else.
	with := `{"agents":{"oc":{"type":"opencode","model":"opencode-go/glm-5.3"},"cli":{"type":"command","command":"claude","args":["-p","--model","{{model}}"]},"bare":{"type":"command","command":"curl","args":["-s"]}},"roles":{"interviewer":"oc","planner":"oc","builder":"oc","reviewer":"cli","moderator":"bare"},"role_models":{"reviewer":"opencode-go/kimi-k3","builder":"opencode-go/minimax-m3"}}`
	c, err := Parse([]byte(with), "")
	if err != nil {
		t.Fatalf("valid role_models rejected: %v", err)
	}
	if got := c.EffectiveModel(RoleReviewer); got != "opencode-go/kimi-k3" {
		t.Errorf("reviewer model = %q", got)
	}
	if got := c.EffectiveModel(RoleBuilder); got != "opencode-go/minimax-m3" {
		t.Errorf("builder model = %q", got)
	}
	// A seat with no override keeps its agent's model — sharing an agent
	// between seats must not leak an override across them.
	if got := c.EffectiveModel(RolePlanner); got != "opencode-go/glm-5.3" {
		t.Errorf("planner model = %q, want the agent's own model", got)
	}
	if got := c.RoleModel(RoleModerator); got != "" {
		t.Errorf("moderator override = %q, want empty", got)
	}
}

// A model override must never apply to a seat the agent does not fill.
func TestRoleModelsArePerSeatNotPerAgent(t *testing.T) {
	with := `{"agents":{"oc":{"type":"opencode","model":"opencode-go/glm-5.3"}},"roles":{"interviewer":"oc","planner":"oc","builder":"oc","reviewer":"oc","moderator":"oc"},"role_models":{"reviewer":"opencode-go/kimi-k3"}}`
	c, err := Parse([]byte(with), "")
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if got := c.EffectiveModel(RoleReviewer); got != "opencode-go/kimi-k3" {
		t.Errorf("reviewer = %q", got)
	}
	// The same agent in four other seats must be untouched.
	for _, role := range []string{RoleInterviewer, RolePlanner, RoleBuilder, RoleModerator} {
		if got := c.EffectiveModel(role); got != "opencode-go/glm-5.3" {
			t.Errorf("%s = %q — the reviewer's override leaked across seats sharing the agent", role, got)
		}
	}
}

func TestRoleModelsValidation(t *testing.T) {
	cases := map[string]string{
		// unknown seat
		`{"agents":{"oc":{"type":"opencode"}},"role_models":{"nonsense":"m"}}`: "unknown role",
		// empty override
		`{"agents":{"oc":{"type":"opencode"}},"role_models":{"builder":"  "}}`: "model is empty",
		// a command agent with no {{model}} would ignore the choice entirely
		`{"agents":{"bare":{"type":"command","command":"curl","args":["-s"]}},
		  "roles":{"builder":"bare"},"role_models":{"builder":"some-model"}}`: "must contain {{model}}",
	}
	for in, want := range cases {
		_, err := Parse([]byte(strings.ReplaceAll(in, "\n", "")), "")
		if err == nil {
			t.Errorf("%s: accepted, want error containing %q", in, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", in, err, want)
		}
		fields := Fields(err)
		if len(fields) == 0 || !strings.HasPrefix(fields[0].Field, "role_models.") {
			t.Errorf("%s: field = %+v, want a role_models.* path", in, fields)
		}
	}
}

// The field path must be precise enough for an editor to highlight the input.
func TestRoleModelFieldPath(t *testing.T) {
	in := `{"agents":{"oc":{"type":"opencode"}},"role_models":{"typo":"m"}}`
	_, err := Parse([]byte(in), "")
	if err == nil {
		t.Fatal("expected an error")
	}
	fields := Fields(err)
	if len(fields) != 1 || fields[0].Field != "role_models.typo" {
		t.Fatalf("fields = %+v", fields)
	}
}

func TestRoleAccessors(t *testing.T) {
	c, err := Parse([]byte(roleModelsBase), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Role(RoleBuilder); got != "oc" {
		t.Errorf("Role(builder) = %q", got)
	}
	if got := c.Role("nonsense"); got != "" {
		t.Errorf("Role(unknown) = %q, want empty", got)
	}
	if IsRole("builder") || IsRole("nope") == false {
		if !IsRole("builder") {
			t.Error("IsRole(builder) = false")
		}
	}
	if len(RoleNames) != 5 {
		t.Errorf("RoleNames = %v", RoleNames)
	}
	// No overrides configured at all must be the normal case.
	if got := c.RoleModels; got != nil {
		t.Errorf("RoleModels = %v, want nil when absent", got)
	}
}

func TestEffectiveModelFallsBackToAgent(t *testing.T) {
	c, err := Parse([]byte(roleModelsBase), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.EffectiveModel(RoleInterviewer); got != "opencode-go/glm-5.3" {
		t.Errorf("interviewer effective model = %q", got)
	}
	// A command agent with no model configured resolves to empty, not to a lie.
	if got := c.EffectiveModel(RoleModerator); got != "" {
		t.Errorf("moderator effective model = %q, want empty", got)
	}
}
