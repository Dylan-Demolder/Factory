package config

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// This file is the scriptable twin of the two org-chart editors: same
// document, same validation, same atomic write under the shared lock. It
// exists so that wiring up a fleet of agents does not require a mouse.

// AgentNameRe is the shape an agent's key must have — the same rule the web
// editor enforces before it lets a draft be added.
var AgentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// AgentTypes lists the adapter kinds an agent may declare.
var AgentTypes = []string{"opencode", "command", "openai"}

// AgentSpec describes one agent to add.
type AgentSpec struct {
	Name         string
	Type         string
	Model        string
	Command      string
	Args         []string
	ReadOnlyArgs []string
	BaseURL      string
	APIKeyEnv    string
	Env          map[string]string
	Timeout      string
	Roles        []string // seats to fill explicitly, in addition to any left empty
	Meta         Meta     // job title, department, tier, notes for org.json
}

// LoadDoc reads a config file as the raw document an editor works with: the
// file exactly as written, unvalidated, so one field can be changed and the
// whole handed back to Save. Keys factory does not understand and any
// {{config_dir}} placeholder survive untouched, because nothing is re-derived
// from the parsed form.
func LoadDoc(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: not a config document: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// AddAgent writes one agent into the config at path.
//
// The document is read as written and handed whole to Save, which validates
// before touching the disk, writes atomically, and takes the lock the web and
// terminal editors share — so an agent added from a shell and one dragged
// onto the org chart cannot interleave into a torn file. A rejected agent
// leaves both factory.json and org.json exactly as they were.
//
// Seats nobody is sitting in are filled with the new agent, so adding a first
// agent leaves a config that actually runs instead of a valid-looking one
// that fails the moment it is used. Seats already taken are untouched unless
// spec.Roles names them. The builder seat is never handed to an `openai`
// agent: it cannot edit files, and validate() would reject the result.
func AddAgent(path string, spec AgentSpec) error {
	if !AgentNameRe.MatchString(spec.Name) {
		return fmt.Errorf("invalid agent name %q: use letters, digits, '.', '_' or '-' (max 64)", spec.Name)
	}
	doc, err := LoadDoc(path)
	if err != nil {
		return err
	}
	agents, _ := doc["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
	}
	if _, exists := agents[spec.Name]; exists {
		return fmt.Errorf("an agent called %q already exists", spec.Name)
	}
	entry, err := spec.entry()
	if err != nil {
		return err
	}
	agents[spec.Name] = entry
	doc["agents"] = agents

	if err := seatNewAgent(doc, spec); err != nil {
		return err
	}

	meta := LoadMeta(path)
	if spec.Meta != (Meta{}) {
		meta[spec.Name] = spec.Meta
	}
	return Save(path, doc, meta)
}

// entry renders the spec as the JSON object factory.json stores. Empty fields
// are omitted rather than written as "" so a minimal agent stays readable and
// DisallowUnknownFields never sees a key it does not know.
func (s AgentSpec) entry() (map[string]any, error) {
	if s.Type == "" {
		return nil, fmt.Errorf("agent %q: --type is required (want %s)", s.Name, strings.Join(AgentTypes, ", "))
	}
	e := map[string]any{"type": s.Type}
	switch s.Type {
	case "opencode", "command", "openai":
	default:
		return nil, fmt.Errorf("agent %q: unknown type %q (want %s)", s.Name, s.Type, strings.Join(AgentTypes, ", "))
	}
	if s.Model != "" {
		e["model"] = s.Model
	}
	if s.Type == "command" && s.Command == "" {
		return nil, fmt.Errorf("agent %q: --command is required for a command agent", s.Name)
	}
	if s.Command != "" {
		e["command"] = s.Command
	}
	if len(s.Args) > 0 {
		e["args"] = toAny(s.Args)
	}
	if len(s.ReadOnlyArgs) > 0 {
		e["readonly_args"] = toAny(s.ReadOnlyArgs)
	}
	if s.BaseURL != "" {
		e["base_url"] = s.BaseURL
	}
	if s.APIKeyEnv != "" {
		e["api_key_env"] = s.APIKeyEnv
	}
	if len(s.Env) > 0 {
		env := make(map[string]any, len(s.Env))
		for k, v := range s.Env {
			env[k] = v
		}
		e["env"] = env
	}
	if s.Timeout != "" {
		e["timeout"] = s.Timeout
	}
	return e, nil
}

// seatNewAgent fills empty seats with the new agent and honours spec.Roles.
//
// Filling is what makes the first `factory agent add` produce something
// runnable: without it the document would carry one agent and five empty
// seats, which validate() rejects as a partial setup. A later agent finds
// every seat taken and so claims nothing unless asked.
func seatNewAgent(doc map[string]any, spec AgentSpec) error {
	roles, _ := doc["roles"].(map[string]any)
	if roles == nil {
		roles = map[string]any{}
	}
	for _, r := range spec.Roles {
		if !IsRole(r) {
			return fmt.Errorf("unknown role %q (want %s)", r, strings.Join(RoleNames, ", "))
		}
	}
	for _, r := range RoleNames {
		current, _ := roles[r].(string)
		taken := current != ""
		asked := contains(spec.Roles, r)

		switch {
		case asked:
			roles[r] = spec.Name
		case taken:
			// Somebody is already in this seat; leave them be.
		case r == RoleBuilder && spec.Type == "openai":
			// Cannot edit files. Staying empty is the honest outcome, and
			// the check below turns it into a message that says what to do.
		default:
			roles[r] = spec.Name
		}
	}
	if b, _ := roles[RoleBuilder].(string); b == "" {
		// Only fatal if this addition is what left it empty: a config whose
		// builder seat is empty for some other reason is rejected by Save
		// anyway, with that error rather than this one.
		if contains(spec.Roles, RoleBuilder) || spec.Type == "openai" {
			return fmt.Errorf("agent %q is type openai, which cannot edit files, so it cannot be the builder — add an opencode or command agent as the builder", spec.Name)
		}
	}
	doc["roles"] = roles
	return nil
}

func toAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
