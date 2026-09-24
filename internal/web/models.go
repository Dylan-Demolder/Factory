package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/dylan-demolder/factory/internal/config"
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

// staticSources is what cannot be enumerated from here: camelStream's fixed
// value, and the CLIs that take any id their owner supports.
//
// Each entry names the flag factory substitutes {{model}} into, so a picker
// can say what to type instead of offering a dropdown full of guesses. These
// are advisory: the user's own args are what actually run.
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
		{
			ID: "gemini", Label: "Gemini CLI", Kind: "text",
			Hint: "--model <model>",
			Note: "Any model id the gemini CLI accepts; factory passes it through as {{model}}.",
		},
		{
			ID: "aider", Label: "aider", Kind: "text",
			Hint: "--model <model>",
			Note: "Any model id aider accepts, including litellm names such as ollama/llama3.",
		},
		{
			ID: "llm", Label: "llm", Kind: "text",
			Hint: "-m <model>",
			Note: "Any model alias or id from `llm models`; factory passes it through as {{model}}.",
		},
		{
			ID: "any-cli", Label: "Any other CLI", Kind: "text",
			Hint: "whatever its own flag is",
			Note: "factory only substitutes {{model}} into your args — put the flag you need there.",
		},
	}
}

// openaiList is the standard shape an OpenAI-compatible /models endpoint
// returns: {"data":[{"id":"…"}]}. Ollama's native API answers with
// {"models":[…]} instead, so both are accepted rather than treating a
// working server as broken.
type openaiList struct {
	Data   []struct{ ID string } `json:"data"`
	Models []struct{ ID string } `json:"models"`
}

// openaiSources asks each configured openai-type agent's endpoint what it
// serves, so the picker offers real ids from the user's own provider instead
// of a hardcoded guess about somebody else's catalogue.
//
// A failure is a warning, never an error: the endpoint may be down, firewalled
// or simply not implement /models, and the editors must still work. The key
// is read from the named env var exactly as the agent itself would read it,
// so nothing new has to be configured to get the list.
func openaiSources(ctx context.Context, cfg *config.Config) ([]modelSource, []string) {
	var (
		sources  []modelSource
		warnings []string
	)
	// Bounded independently of the caller: a hanging endpoint must not hold
	// the picker open, and these run one after another.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	names := make([]string, 0, len(cfg.Agents))
	for n := range cfg.Agents {
		names = append(names, n)
	}
	sort.Strings(names) // map order is random; the response must not be

	for _, name := range names {
		a := cfg.Agents[name]
		if a.Type != "openai" || a.BaseURL == "" {
			continue
		}
		ids, err := fetchOpenAIModels(ctx, a.BaseURL, a.APIKeyEnv)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", name, err))
			// Still offer the seat a way to name a model.
			sources = append(sources, modelSource{
				ID: "agent:" + name, Label: name + " (endpoint)", Kind: "text",
				Hint: "model id", Note: fmt.Sprintf("Could not list models from %s; type an id.", a.BaseURL),
			})
			continue
		}
		if len(ids) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: %s/models returned no models", name, a.BaseURL))
			continue
		}
		sources = append(sources, modelSource{
			ID: "agent:" + name, Label: name + " (endpoint)", Kind: "pick",
			Models: ids,
			Note:   fmt.Sprintf("Listed live from %s.", a.BaseURL),
		})
	}
	return sources, warnings
}

// fetchOpenAIModels GETs {base}/models and pulls the ids out. The key is
// optional — some local endpoints need none — and is taken from the env var
// the agent names, so it never appears in config or in this response.
func fetchOpenAIModels(ctx context.Context, baseURL, keyEnv string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	if keyEnv != "" {
		if key := os.Getenv(keyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d from /models", resp.StatusCode)
	}
	var list openaiList
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("reading /models: %w", err)
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(list.Data)+len(list.Models))
	for _, m := range append(list.Data, list.Models...) {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	return ids, nil
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

	// Enumerate the user's own endpoints. A config that will not load is not
	// this endpoint's problem — the static sources above still render.
	if cfg, _, err := config.Resolve(s.opt.ConfigPath, ""); err == nil {
		live, warns := openaiSources(r.Context(), cfg)
		resp.Sources = append(resp.Sources, live...)
		resp.Warnings = append(resp.Warnings, warns...)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(resp)
}
