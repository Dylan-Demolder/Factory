package tui

import (
	"strings"
	"testing"

	"github.com/dylan-demolder/factory/internal/config"
)

// With a catalogue available, an opencode agent must offer it — that is the
// difference between a drop-down of available options and a text field.
func TestAgentModelChoicesPreferCatalogue(t *testing.T) {
	s := newTestOrg()
	s.available = []string{"opencode-go/aaa-model", "opencode-go/bbb-model"}

	got := s.availableModels("aaa-opencode")
	for _, want := range []string{"opencode-go/aaa-model", "opencode-go/bbb-model"} {
		if orgIndexOf(got, want) < 0 {
			t.Errorf("opencode agent choices missing catalogue id %q: %v", want, got)
		}
	}

	// A command agent's id comes from its own CLI, so opencode's catalogue
	// must NOT be offered for it — that would invite an id the CLI rejects.
	cmdChoices := s.availableModels("zzz-command")
	if orgIndexOf(cmdChoices, "opencode-go/aaa-model") >= 0 {
		t.Errorf("command agent was offered an opencode catalogue id: %v", cmdChoices)
	}
}

// Without a catalogue (fetch failed) the picker still works from what is
// configured, rather than refusing to open.
func TestAgentModelChoicesFallBackToConfigured(t *testing.T) {
	s := newTestOrg() // available is nil
	got := s.availableModels("aaa-opencode")
	if orgIndexOf(got, "opencode-go/glm-5.3") < 0 {
		t.Errorf("configured model missing from fallback choices: %v", got)
	}
}

// A model that would be accepted and then ignored must be refused, naming
// the exact fix — one keystroke earlier than config.validate would.
func TestModelBlockRefusesInertChoices(t *testing.T) {
	s := newTestOrg()
	s.agents["camel-x"] = config.Agent{
		Type: "command", Command: "python3",
		Env: map[string]string{"CAMEL_BASE_URL": "https://stream.camelai.com/v1"},
	}
	s.agents["claude-cli"] = config.Agent{
		Type: "command", Command: "claude", Args: []string{"--model", "{{model}}"},
	}

	cases := map[string]struct {
		agent   string
		blocked bool
		want    string
	}{
		"opencode agent":            {agent: "aaa-opencode", blocked: false},
		"openai agent":              {agent: "mmm-openai", blocked: false},
		"command with {{model}}":    {agent: "claude-cli", blocked: false},
		"camelStream agent":         {agent: "camel-x", blocked: true, want: "auto"},
		"command without {{model}}": {agent: "zzz-command", blocked: true, want: "{{model}}"},
	}
	for name, c := range cases {
		got := s.modelBlock(c.agent)
		if c.blocked {
			if got == "" {
				t.Errorf("%s: expected a refusal, got none", name)
				continue
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("%s: refusal %q does not mention %q", name, got, c.want)
			}
		} else if got != "" {
			t.Errorf("%s: refused (%q) but a model choice is legitimate here", name, got)
		}
	}
}

func TestSetAgentModelMarksTheStateDirty(t *testing.T) {
	s := newTestOrg()
	if _, ok := s.setAgentModel("aaa-opencode", "opencode-go/kimi-k3"); !ok {
		t.Fatal("setting an opencode agent's model was refused")
	}
	if s.agents["aaa-opencode"].Model != "opencode-go/kimi-k3" {
		t.Errorf("model not updated: %v", s.agents["aaa-opencode"].Model)
	}
	if !s.dirty || !s.modelEdited["aaa-opencode"] {
		t.Error("change did not mark the state dirty / model-edited — it would not be saved")
	}

	// Clearing is legitimate for opencode: "" means the engine default.
	if _, ok := s.setAgentModel("aaa-opencode", ""); !ok {
		t.Error("clearing an opencode agent's model was refused")
	}
	if s.agents["aaa-opencode"].Model != "" {
		t.Errorf("clear did not take: %q", s.agents["aaa-opencode"].Model)
	}

	// A camel agent's model is inert (fleet, `auto`), so it is refused.
	s.agents["camel-x"] = config.Agent{
		Type: "command", Command: "python3",
		Env: map[string]string{"CAMEL_BASE_URL": "https://stream.camelai.com/v1"},
	}
	if _, ok := s.setAgentModel("camel-x", "whatever"); ok {
		t.Error("camel agent accepted a model change that could never take effect")
	}
	if _, ok := s.setAgentModel("no-such-agent", "x"); ok {
		t.Error("unknown agent accepted a model change")
	}
}

// The save path writes the raw document, so a cleared model must become an
// absent key (omitempty), not `"model": ""`.
func TestEditedDocDropsClearedModel(t *testing.T) {
	s := newTestOrg()
	s.doc = map[string]any{
		"agents": map[string]any{
			"aaa-opencode": map[string]any{"type": "opencode", "model": "old-model"},
		},
	}

	modelOf := func(t *testing.T, doc map[string]any) (string, bool) {
		t.Helper()
		agents, _ := doc["agents"].(map[string]any)
		obj, _ := agents["aaa-opencode"].(map[string]any)
		v, present := obj["model"]
		s, _ := v.(string)
		return s, present
	}

	s.setAgentModel("aaa-opencode", "new-model")
	if v, present := modelOf(t, s.editedDoc()); !present || v != "new-model" {
		t.Errorf("edited model = %q present=%v, want new-model", v, present)
	}

	s.setAgentModel("aaa-opencode", "")
	if v, present := modelOf(t, s.editedDoc()); present {
		t.Errorf("cleared model written as %q — want the key removed (omitempty)", v)
	}
}

// A role's pin that isn't in the catalogue must stay selectable, or cycling
// past it would silently drop a hand-edited value.
func TestRoleChoicesKeepAPinOutsideTheCatalogue(t *testing.T) {
	s := newTestOrg()
	s.available = nil // no catalogue: only configured models are known
	s.roleModels = map[string]string{"builder": "claude-opus-4-5"}

	choices := s.modelChoices(2) // roleKeys[2] == builder
	if choices[0] != "" {
		t.Errorf("first choice = %q, want \"\" (inherit)", choices[0])
	}
	if orgIndexOf(choices, "claude-opus-4-5") < 0 {
		t.Errorf("pin dropped from choices: %v", choices)
	}
}
