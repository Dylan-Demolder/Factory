// Package agent adapts external AI agents (opencode, CLIs, HTTP APIs) to a
// single request/response interface.
package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/dylan-demolder/factory-/internal/config"
)

// Request is one prompt sent to an agent.
type Request struct {
	// Stage identifies the pipeline step (e.g. "interview", "build"); used
	// for logging and by test fakes.
	Stage  string
	System string
	Prompt string
	// Dir is the working directory (the project repository).
	Dir string
	// ReadOnly asks the agent not to modify files (discussion, review).
	ReadOnly bool
}

// Agent answers prompts.
type Agent interface {
	Name() string
	Run(ctx context.Context, req Request) (string, error)
}

// Compose flattens system and user prompt for agents without a system slot.
func Compose(req Request) string {
	if req.System == "" {
		return req.Prompt
	}
	return req.System + "\n\n---\n\n" + req.Prompt
}

// New builds an agent from config.
func New(name string, c config.Agent, defaultTimeout time.Duration) (Agent, error) {
	timeout := c.Timeout.Duration
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	switch c.Type {
	case "opencode":
		return newOpencode(name, c, timeout), nil
	case "command":
		return newCommand(name, c, timeout), nil
	case "openai":
		return newOpenAI(name, c, timeout), nil
	}
	return nil, fmt.Errorf("agent %q: unknown type %q", name, c.Type)
}

// NewAll builds every configured agent.
func NewAll(cfg *config.Config) (map[string]Agent, error) {
	out := make(map[string]Agent, len(cfg.Agents))
	for name, c := range cfg.Agents {
		a, err := New(name, c, cfg.Limits.AgentTimeout.Duration)
		if err != nil {
			return nil, err
		}
		out[name] = a
	}
	return out, nil
}
