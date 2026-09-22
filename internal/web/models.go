package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Model pickers need to know what they can offer. Some providers can be
// enumerated (`opencode models`), some publish a fixed value (camelStream is a
// fleet that only accepts `auto`), and some take any id their CLI accepts —
// Claude and ChatGPT have no list command, so those are free-text fields with
// the flag spelled out rather than a dropdown full of guesses.

// modelSource is one place a model can come from.
type modelSource struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Kind   string   `json:"kind"` // "pick" = choose from Models, "text" = type an id
	Models []string `json:"models,omitempty"`
	Hint   string   `json:"hint,omitempty"`
	Note   string   `json:"note,omitempty"`
}

// modelsResponse is GET /api/models.
type modelsResponse struct {
	Sources  []modelSource `json:"sources"`
	Warnings []string      `json:"warnings,omitempty"`
}

// opencodeModels shells out to `opencode models`. A missing or broken
// binary is a warning, not a failure: the editors still work, they just
// offer free text instead of a list.
func (s *Server) opencodeModels(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "opencode", "models").Output()
	if err != nil {
		return nil, fmt.Errorf("opencode models: %w", err)
	}
	return parseModelList(string(out)), nil
}

// parseModelList turns `opencode models` output — one bare id per line — into
// a sorted, de-duplicated list. Blank lines and repeats are dropped so a
// provider listing the same id twice cannot double it up in the picker.
func parseModelList(out string) []string {
	seen := map[string]bool{}
	ids := make([]string, 0, 40)
	for _, line := range strings.Split(out, "\n") {
		id := strings.TrimSpace(line)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// staticSources is what cannot be enumerated: camelStream's fixed value and
// the CLIs that accept any id.
func staticSources() []modelSource {
	return []modelSource{
		{
			ID: "camel", Label: "camelStream", Kind: "pick",
			Models: []string{"auto"},
			Note:   "Serves a fleet, so only auto is accepted — a pinned model is not available on a standard subscription.",
		},
		{
			ID: "claude", Label: "Claude Code", Kind: "text",
			Hint: "--model <model>",
			Note: "Any model id the claude CLI accepts; factory passes it through as {{model}}.",
		},
		{
			ID: "codex", Label: "ChatGPT (codex)", Kind: "text",
			Hint: "-m <model>",
			Note: "Any model id the codex CLI accepts; factory passes it through as {{model}}.",
		},
	}
}

// handleModels reports every place a role's model can come from.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	resp := modelsResponse{Sources: staticSources()}

	ids, err := s.ListModels(r.Context())
	switch {
	case err != nil:
		resp.Warnings = append(resp.Warnings, err.Error())
	case len(ids) == 0:
		resp.Warnings = append(resp.Warnings, "opencode models returned no models")
	default:
		resp.Sources = append([]modelSource{{
			ID: "opencode", Label: "OpenCode Go", Kind: "pick", Models: ids,
		}}, resp.Sources...)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}
