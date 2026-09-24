package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFresh drops an empty config — what `factory init` produces — in a
// temp dir and returns its path.
func writeFresh(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	if err := os.WriteFile(path, []byte(ExampleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The whole point of the command: one flag-typed agent added from a shell
// must leave a config that loads, runs, and holds the seat.
func TestAddAgentConfiguresAWorkingSetup(t *testing.T) {
	path := writeFresh(t)

	err := AddAgent(path, AgentSpec{
		Name:    "oc",
		Type:    "opencode",
		Model:   "opencode-go/glm-5.3",
		Timeout: "45m",
	})
	if err != nil {
		t.Fatalf("AddAgent: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("the config after adding an agent does not load: %v", err)
	}
	if !c.Configured() {
		t.Fatal("still unconfigured after adding an agent")
	}
	if c.Agents["oc"].Model != "opencode-go/glm-5.3" {
		t.Errorf("model = %q", c.Agents["oc"].Model)
	}
	// A first agent must claim every seat, or the config validates now and
	// fails the first time a project is created.
	for _, r := range RoleNames {
		if got := c.Role(r); got != "oc" {
			t.Errorf("role %s = %q, want oc (a lone agent has to fill the panel)", r, got)
		}
	}
}

// A second agent must not evict anyone: seats are filled once, then left to
// the org chart.
func TestAddAgentLeavesOccupiedSeatsAlone(t *testing.T) {
	path := writeFresh(t)
	if err := AddAgent(path, AgentSpec{Name: "oc", Type: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := AddAgent(path, AgentSpec{Name: "cli", Type: "command", Command: "claude", Args: []string{"-p", "{{prompt}}"}}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range RoleNames {
		if got := c.Role(r); got != "oc" {
			t.Errorf("role %s = %q, want oc — adding a second agent took a seat", r, got)
		}
	}
}

// --role is how a seat gets reassigned without the GUI.
func TestAddAgentExplicitRoleOverrides(t *testing.T) {
	path := writeFresh(t)
	if err := AddAgent(path, AgentSpec{Name: "oc", Type: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := AddAgent(path, AgentSpec{
		Name: "qa", Type: "command", Command: "claude",
		Args: []string{"-p", "{{prompt}}"}, Roles: []string{RoleReviewer},
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Role(RoleReviewer) != "qa" {
		t.Errorf("reviewer = %q, want qa", c.Role(RoleReviewer))
	}
	if c.Role(RoleBuilder) != "oc" {
		t.Errorf("builder = %q, want oc — an explicit seat claim leaked", c.Role(RoleBuilder))
	}
}

// An openai endpoint cannot edit files, so it cannot build. Asking it to must
// fail with that reason rather than writing a config that trips over itself.
func TestAddAgentRefusesOpenAIAsBuilder(t *testing.T) {
	path := writeFresh(t)
	err := AddAgent(path, AgentSpec{
		Name: "gpt", Type: "openai",
		BaseURL: "https://api.example.com/v1", Model: "some-model",
	})
	if err == nil {
		t.Fatal("accepted an openai agent as the only agent, leaving no builder")
	}
	if !strings.Contains(err.Error(), "cannot edit files") {
		t.Errorf("error = %q, want it to say why", err)
	}
	// Nothing must have been written: the file is still the empty config.
	c, lerr := Load(path)
	if lerr != nil {
		t.Fatal(lerr)
	}
	if c.Configured() {
		t.Error("a rejected agent was written anyway")
	}
}

// Once something can build, an openai agent is fine in the other seats.
func TestAddAgentAllowsOpenAIAlongsideABuilder(t *testing.T) {
	path := writeFresh(t)
	if err := AddAgent(path, AgentSpec{Name: "oc", Type: "opencode"}); err != nil {
		t.Fatal(err)
	}
	if err := AddAgent(path, AgentSpec{
		Name: "gpt", Type: "openai",
		BaseURL: "https://api.example.com/v1", Model: "some-model",
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Role(RoleBuilder) != "oc" {
		t.Errorf("builder = %q, want oc", c.Role(RoleBuilder))
	}
	if c.Agents["gpt"].BaseURL == "" {
		t.Error("openai agent lost its base_url")
	}
}

// A command agent with no command is exactly the config that would sit there
// looking fine and fail on first contact.
func TestAddAgentRejectsIncompleteInput(t *testing.T) {
	path := writeFresh(t)
	cases := []struct {
		spec AgentSpec
		want string
	}{
		{AgentSpec{Name: "x", Type: "command"}, "--command is required"},
		{AgentSpec{Name: "x", Type: "banana"}, "unknown type"},
		{AgentSpec{Name: "x"}, "--type is required"},
		{AgentSpec{Name: "bad name!", Type: "opencode"}, "invalid agent name"},
		{AgentSpec{Name: "oc", Type: "opencode", Roles: []string{"nope"}}, "unknown role"},
	}
	for _, tc := range cases {
		err := AddAgent(path, tc.spec)
		if err == nil {
			t.Errorf("%+v: accepted, want error containing %q", tc.spec, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %q, want %q", tc.spec, err, tc.want)
		}
	}
	// Every rejection must leave the file untouched.
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Configured() {
		t.Error("a rejected agent was written anyway")
	}
}

// Adding the same agent twice is a typo, not a merge.
func TestAddAgentRejectsDuplicate(t *testing.T) {
	path := writeFresh(t)
	if err := AddAgent(path, AgentSpec{Name: "oc", Type: "opencode"}); err != nil {
		t.Fatal(err)
	}
	err := AddAgent(path, AgentSpec{Name: "oc", Type: "opencode"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want 'already exists'", err)
	}
}

// Whatever an editor wrote that factory does not understand must survive a
// CLI add, and placeholders must stay symbolic — the same promise Save makes
// to the two GUI editors.
func TestAddAgentPreservesTheRestOfTheDocument(t *testing.T) {
	path := writeFresh(t)
	raw := `{"agents":{"oc":{"type":"command","command":"python3","args":["{{config_dir}}/adapters/x.py"]}},
	        "roles":{"builder":"oc","interviewer":"oc","planner":"oc","reviewer":"oc","moderator":"oc"},
	        "test_command":"go test ./...","roundtable":{"rounds":3,"participants":[]}}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddAgent(path, AgentSpec{Name: "qa", Type: "command", Command: "claude"}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}

	doc, err := LoadDoc(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc["test_command"] != "go test ./..." {
		t.Errorf("test_command = %v, want it preserved", doc["test_command"])
	}
	// {{config_dir}} must not have been frozen into an absolute path.
	if !strings.Contains(agentField(doc, "oc", "args"), "{{config_dir}}") {
		t.Errorf("placeholder was expanded: %v", doc["agents"])
	}
	c, err := Load(path)
	if err != nil {
		t.Errorf("config no longer loads: %v", err)
		return
	}
	if c.Roundtable.Rounds != 3 {
		t.Errorf("rounds = %d, want 3", c.Roundtable.Rounds)
	}
	// The original agent keeps its seat.
	if c.Role(RoleBuilder) != "oc" {
		t.Errorf("builder = %q, want oc", c.Role(RoleBuilder))
	}
}

// agentField digs a field out of the stored agents map for assertion.
func agentField(doc map[string]any, agent, field string) string {
	agents, _ := doc["agents"].(map[string]any)
	a, _ := agents[agent].(map[string]any)
	switch v := a[field].(type) {
	case string:
		return v
	case []any:
		var out []string
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return strings.Join(out, " ")
	}
	return ""
}

// Env goes in as shell-style K=V pairs and must come back as a map, with
// secrets landing in the file rather than being dropped.
func TestAddAgentCarriesEnvironment(t *testing.T) {
	path := writeFresh(t)
	if err := AddAgent(path, AgentSpec{
		Name: "cli", Type: "command", Command: "mycli",
		Env: map[string]string{"MY_API_KEY": "sk-test", "MY_REGION": "eu"},
	}); err != nil {
		t.Fatalf("AddAgent: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := c.Agents["cli"].Env
	if got["MY_API_KEY"] != "sk-test" || got["MY_REGION"] != "eu" {
		t.Errorf("env = %v", got)
	}
	// And the file must not become unloadable because a value looks secret.
	if !SecretEnv("MY_API_KEY", got["MY_API_KEY"]) {
		t.Error("test premise broken: SecretEnv does not flag this key")
	}
}

// A job title belongs in org.json, not factory.json — that separation is why
// DisallowUnknownFields does not reject it.
func TestAddAgentWritesOrgMetadata(t *testing.T) {
	path := writeFresh(t)
	if err := AddAgent(path, AgentSpec{
		Name: "oc", Type: "opencode",
		Meta: Meta{Title: "Senior Engineer", Dept: "Engineering"},
	}); err != nil {
		t.Fatal(err)
	}
	meta := LoadMeta(path)
	if meta["oc"].Title != "Senior Engineer" || meta["oc"].Dept != "Engineering" {
		t.Errorf("meta = %+v", meta["oc"])
	}
	// factory.json itself must not have grown the key.
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "Senior Engineer") {
		t.Error("a job title leaked into factory.json")
	}
}
