package web

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseModelList(t *testing.T) {
	in := "  opencode-go/z  \n\nopencode-go/a\nopencode-go/z\n   \nopencode-go/a\n"
	got := parseModelList(in)
	want := []string{"opencode-go/a", "opencode-go/z"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v (sorted, trimmed, de-duplicated)", got, want)
		}
	}
	if parseModelList("") == nil {
		// empty output should be an empty list, and the handler turns that
		// into a warning rather than pretending the provider is healthy
		t.Log("empty output yields an empty slice")
	}
}

func TestModelsEndpoint(t *testing.T) {
	e := setup(t, nil)
	e.login()
	e.srv.ListModels = func(context.Context) ([]string, error) {
		return []string{"opencode-go/kimi-k3", "opencode-go/glm-5.3"}, nil
	}

	code, out := e.do("GET", "/api/models", nil)
	if code != 200 {
		t.Fatalf("status = %d: %v", code, out)
	}
	if _, ok := out["warnings"]; ok {
		t.Errorf("unexpected warnings: %v", out["warnings"])
	}

	sources := out["sources"].([]any)
	if len(sources) < 4 {
		t.Fatalf("got %d sources, want opencode + camel + claude + codex", len(sources))
	}
	first := sources[0].(map[string]any)
	if first["id"] != "opencode" || first["kind"] != "pick" {
		t.Errorf("first source = %v", first)
	}
	models := first["models"].([]any)
	if len(models) != 2 {
		t.Errorf("models = %v", models)
	}

	// camelStream is a fleet: only `auto` may be offered, or the picker
	// would let someone configure a pin the endpoint rejects.
	var camel, claude, codex map[string]any
	for _, s := range sources {
		m := s.(map[string]any)
		switch m["id"] {
		case "camel":
			camel = m
		case "claude":
			claude = m
		case "codex":
			codex = m
		}
	}
	if camel == nil || camel["kind"] != "pick" {
		t.Fatalf("camel source = %v", camel)
	}
	cm := camel["models"].([]any)
	if len(cm) != 1 || cm[0] != "auto" {
		t.Errorf("camel models = %v, want exactly [auto]", cm)
	}
	// The two CLIs have no list command, so they must be free text with the
	// flag spelled out — never a dropdown of guesses.
	for _, s := range []struct {
		src  map[string]any
		hint string
	}{{claude, "--model"}, {codex, "-m"}} {
		if s.src == nil {
			t.Errorf("%s source missing", s.hint)
			continue
		}
		if s.src["kind"] != "text" {
			t.Errorf("%s kind = %v, want text", s.hint, s.src["kind"])
		}
		if h, _ := s.src["hint"].(string); !strings.Contains(h, s.hint) {
			t.Errorf("%s hint = %v, want it to contain %q", s.hint, s.src["hint"], s.hint)
		}
	}
}

// A provider that cannot be enumerated must degrade to free text, not take
// the whole screen down with it.
func TestModelsEndpointSurvivesProviderFailure(t *testing.T) {
	e := setup(t, nil)
	e.login()
	e.srv.ListModels = func(context.Context) ([]string, error) {
		return nil, errors.New("opencode models: executable file not found")
	}

	code, out := e.do("GET", "/api/models", nil)
	if code != 200 {
		t.Fatalf("status = %d: %v — a missing binary should not be an error", code, out)
	}
	warnings := out["warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), "executable file not found") {
		t.Errorf("warnings = %v", warnings)
	}
	sources := out["sources"].([]any)
	for _, s := range sources {
		if s.(map[string]any)["id"] == "opencode" {
			t.Error("opencode source present despite the provider failing")
		}
	}
	if len(sources) != 3 {
		t.Errorf("static sources = %d, want 3 (camel, claude, codex)", len(sources))
	}
}

func TestModelsEndpointEmptyListWarns(t *testing.T) {
	e := setup(t, nil)
	e.login()
	e.srv.ListModels = func(context.Context) ([]string, error) { return nil, nil }

	code, out := e.do("GET", "/api/models", nil)
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	warnings := out["warnings"].([]any)
	if len(warnings) != 1 || !strings.Contains(warnings[0].(string), "no models") {
		t.Errorf("warnings = %v, want an empty-list warning", warnings)
	}
}

// Model ids are configuration, not secrets — but the endpoint is still part
// of the authenticated API surface.
func TestModelsEndpointRequiresAuth(t *testing.T) {
	e := setup(t, nil)
	if code, _ := e.do("GET", "/api/models", nil); code != 401 {
		t.Fatalf("status = %d, want 401", code)
	}
}
