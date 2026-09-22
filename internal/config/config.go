// Package config loads factory's agent/role configuration.
package config

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed assets/factory.example.json
var ExampleJSON string

//go:embed assets/camel_agent.py
var CamelAdapter string

// Duration is a time.Duration that reads "30m"-style strings from JSON.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		d.Duration = v
		return nil
	}
	var secs float64
	if err := json.Unmarshal(b, &secs); err != nil {
		return fmt.Errorf("duration must be a string like \"30m\" or seconds")
	}
	d.Duration = time.Duration(secs * float64(time.Second))
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// Agent describes how to reach one agent.
//
//	type "opencode": runs `opencode run` in the project directory (can edit files).
//	type "command":  runs any CLI; prompt goes to {{prompt}} / {{prompt_file}} in args, else stdin.
//	type "openai":   any OpenAI-compatible /chat/completions endpoint (text only).
type Agent struct {
	Type         string            `json:"type"`
	Model        string            `json:"model,omitempty"`
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	ReadOnlyArgs []string          `json:"readonly_args,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	BaseURL      string            `json:"base_url,omitempty"`
	APIKeyEnv    string            `json:"api_key_env,omitempty"`
	Timeout      Duration          `json:"timeout,omitempty"`
}

// Participant is one seat at a roundtable.
type Participant struct {
	Agent   string `json:"agent"`
	Persona string `json:"persona"`
}

// Roles maps pipeline jobs to agent names.
type Roles struct {
	Interviewer string `json:"interviewer"`
	Planner     string `json:"planner"`
	Builder     string `json:"builder"`
	Reviewer    string `json:"reviewer"`
	Moderator   string `json:"moderator"`
}

type Roundtable struct {
	Participants []Participant `json:"participants"`
	Rounds       int           `json:"rounds"`
	OnSpec       *bool         `json:"on_spec,omitempty"`
	OnPlan       *bool         `json:"on_plan,omitempty"`
	OnTasks      *bool         `json:"on_tasks,omitempty"`
}

type Limits struct {
	MaxInterviewRounds  int      `json:"max_interview_rounds"`
	MaxTaskAttempts     int      `json:"max_task_attempts"`
	MaxAcceptanceRounds int      `json:"max_acceptance_rounds"`
	AgentRetries        int      `json:"agent_retries"`
	AgentTimeout        Duration `json:"agent_timeout"`
	TestTimeout         Duration `json:"test_timeout"`
}

type Config struct {
	Agents        map[string]Agent `json:"agents"`
	Roles         Roles            `json:"roles"`
	Roundtable    Roundtable       `json:"roundtable"`
	Limits        Limits           `json:"limits"`
	TestCommand   string           `json:"test_command,omitempty"`
	NotifyCommand string           `json:"notify_command,omitempty"`

	// Dir is the directory the config was loaded from ({{config_dir}}).
	Dir string `json:"-"`
}

func on(b *bool) bool { return b == nil || *b }

func (r Roundtable) SpecEnabled() bool  { return on(r.OnSpec) }
func (r Roundtable) PlanEnabled() bool  { return on(r.OnPlan) }
func (r Roundtable) TasksEnabled() bool { return on(r.OnTasks) }

// Parse decodes and validates a config. dir resolves {{config_dir}}.
func Parse(data []byte, dir string) (*Config, error) {
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.Dir = dir
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Load reads a config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data, filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	l := &c.Limits
	if l.MaxInterviewRounds <= 0 {
		l.MaxInterviewRounds = 3
	}
	if l.MaxTaskAttempts <= 0 {
		l.MaxTaskAttempts = 4
	}
	if l.MaxAcceptanceRounds <= 0 {
		l.MaxAcceptanceRounds = 3
	}
	if l.AgentRetries < 0 {
		l.AgentRetries = 0
	}
	if l.AgentTimeout.Duration <= 0 {
		l.AgentTimeout.Duration = 30 * time.Minute
	}
	if l.TestTimeout.Duration <= 0 {
		l.TestTimeout.Duration = 15 * time.Minute
	}
	if c.Roundtable.Rounds <= 0 {
		c.Roundtable.Rounds = 2
	}
	r := &c.Roles
	first := r.Builder
	if first == "" {
		for name, a := range c.Agents {
			if a.Type == "opencode" {
				first = name
			}
		}
	}
	if r.Builder == "" {
		r.Builder = first
	}
	for _, p := range []*string{&r.Interviewer, &r.Planner, &r.Reviewer, &r.Moderator} {
		if *p == "" {
			*p = first
		}
	}
	for name, a := range c.Agents {
		for i, arg := range a.Args {
			a.Args[i] = c.Expand(arg)
		}
		a.Command = c.Expand(a.Command)
		c.Agents[name] = a
	}
}

// Expand substitutes {{config_dir}}.
func (c *Config) Expand(s string) string {
	return strings.ReplaceAll(s, "{{config_dir}}", c.Dir)
}

func (c *Config) validate() error {
	var errs []error
	if len(c.Agents) == 0 {
		errs = append(errs, errors.New("no agents configured"))
	}
	for name, a := range c.Agents {
		switch a.Type {
		case "opencode":
		case "command":
			if a.Command == "" {
				errs = append(errs, fmt.Errorf("agent %q: command is required", name))
			}
		case "openai":
			if a.BaseURL == "" || a.Model == "" {
				errs = append(errs, fmt.Errorf("agent %q: base_url and model are required", name))
			}
		default:
			errs = append(errs, fmt.Errorf("agent %q: unknown type %q (want opencode, command or openai)", name, a.Type))
		}
	}
	check := func(role, name string) {
		if name == "" {
			errs = append(errs, fmt.Errorf("role %s: no agent assigned", role))
		} else if _, ok := c.Agents[name]; !ok {
			errs = append(errs, fmt.Errorf("role %s: unknown agent %q", role, name))
		}
	}
	check("interviewer", c.Roles.Interviewer)
	check("planner", c.Roles.Planner)
	check("builder", c.Roles.Builder)
	check("reviewer", c.Roles.Reviewer)
	check("moderator", c.Roles.Moderator)
	if b, ok := c.Agents[c.Roles.Builder]; ok && b.Type == "openai" {
		errs = append(errs, fmt.Errorf("role builder: agent %q is type openai and cannot edit files; use opencode or a command agent", c.Roles.Builder))
	}
	for i, p := range c.Roundtable.Participants {
		check(fmt.Sprintf("roundtable.participants[%d]", i), p.Agent)
	}
	return errors.Join(errs...)
}

// Resolve finds the config to use. Order: explicit path, $FACTORY_CONFIG,
// <projectRoot>/.factory/config.json (if projectRoot given), ./factory.json,
// ~/.config/factory/factory.json.
func Resolve(explicit, projectRoot string) (*Config, string, error) {
	var candidates []string
	if explicit != "" {
		candidates = []string{explicit}
	} else {
		if env := os.Getenv("FACTORY_CONFIG"); env != "" {
			candidates = append(candidates, env)
		}
		if projectRoot != "" {
			candidates = append(candidates, filepath.Join(projectRoot, ".factory", "config.json"))
		}
		candidates = append(candidates, "factory.json")
		if dir, err := os.UserConfigDir(); err == nil {
			candidates = append(candidates, filepath.Join(dir, "factory", "factory.json"))
		}
	}
	for _, path := range candidates {
		if _, err := os.Stat(path); err != nil {
			if explicit != "" {
				return nil, "", err
			}
			continue
		}
		c, err := Load(path)
		if err != nil {
			return nil, "", err
		}
		abs, _ := filepath.Abs(path)
		return c, abs, nil
	}
	return nil, "", errors.New("no config found; run `factory init` to create factory.json")
}
