package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dylan-demolder/factory-/internal/config"
	"github.com/dylan-demolder/factory-/internal/proc"
)

// maxArgPrompt is the largest prompt passed inline as an argument; Linux
// caps a single argv string at 128 KiB.
const maxArgPrompt = 96 * 1024

// argBuilder turns a request into argv and optional stdin.
type argBuilder func(req Request, prompt string) (args []string, stdin string, cleanup func(), err error)

// Command runs an external CLI per request.
type Command struct {
	name    string
	command string
	env     map[string]string
	timeout time.Duration
	build   argBuilder
}

func (c *Command) Name() string { return c.name }

func newCommand(name string, cfg config.Agent, timeout time.Duration) *Command {
	c := &Command{name: name, command: cfg.Command, env: cfg.Env, timeout: timeout}
	c.build = func(req Request, prompt string) ([]string, string, func(), error) {
		cleanup := func() {}
		usesPrompt := false
		args := make([]string, 0, len(cfg.Args)+len(cfg.ReadOnlyArgs))
		for _, a := range cfg.Args {
			if strings.Contains(a, "{{prompt_file}}") {
				f, err := os.CreateTemp("", "factory-prompt-*.md")
				if err != nil {
					return nil, "", cleanup, err
				}
				if _, err := f.WriteString(prompt); err != nil {
					f.Close()
					return nil, "", cleanup, err
				}
				f.Close()
				path := f.Name()
				prev := cleanup
				cleanup = func() { prev(); os.Remove(path) }
				a = strings.ReplaceAll(a, "{{prompt_file}}", path)
				usesPrompt = true
			}
			if strings.Contains(a, "{{prompt}}") {
				a = strings.ReplaceAll(a, "{{prompt}}", prompt)
				usesPrompt = true
			}
			a = strings.ReplaceAll(a, "{{model}}", cfg.Model)
			a = strings.ReplaceAll(a, "{{dir}}", req.Dir)
			args = append(args, a)
		}
		if req.ReadOnly {
			args = append(args, cfg.ReadOnlyArgs...)
		}
		if usesPrompt {
			return args, "", cleanup, nil
		}
		return args, prompt, cleanup, nil
	}
	return c
}

// newOpencode drives `opencode run` non-interactively.
func newOpencode(name string, cfg config.Agent, timeout time.Duration) *Command {
	command := cfg.Command
	if command == "" {
		command = "opencode"
	}
	readOnly := cfg.ReadOnlyArgs
	if readOnly == nil {
		// opencode's built-in "plan" agent cannot edit files.
		readOnly = []string{"--agent", "plan"}
	}
	c := &Command{name: name, command: command, env: cfg.Env, timeout: timeout}
	c.build = func(req Request, prompt string) ([]string, string, func(), error) {
		cleanup := func() {}
		args := []string{"run"}
		if cfg.Model != "" {
			args = append(args, "--model", cfg.Model)
		}
		if req.ReadOnly {
			args = append(args, readOnly...)
		}
		args = append(args, cfg.Args...)
		msg := prompt
		if len(prompt) > maxArgPrompt {
			dir := filepath.Join(req.Dir, ".factory", "tmp")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, "", cleanup, err
			}
			f, err := os.CreateTemp(dir, "prompt-*.md")
			if err != nil {
				return nil, "", cleanup, err
			}
			_, werr := f.WriteString(prompt)
			f.Close()
			if werr != nil {
				return nil, "", cleanup, werr
			}
			path := f.Name()
			cleanup = func() { os.Remove(path) }
			rel, _ := filepath.Rel(req.Dir, path)
			msg = fmt.Sprintf("Your complete instructions are in the file %s (too long to pass inline). Read the whole file first, then follow it exactly, including the required response format.", rel)
		}
		args = append(args, msg)
		return args, "", cleanup, nil
	}
	return c
}

func (c *Command) Run(ctx context.Context, req Request) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args, stdin, cleanup, err := c.build(req, Compose(req))
	defer cleanup()
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", c.name, err)
	}
	cmd := exec.CommandContext(ctx, c.command, args...)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), envList(c.env)...)
	cmd.Env = append(cmd.Env, "FACTORY_STAGE="+req.Stage, fmt.Sprintf("FACTORY_READONLY=%t", req.ReadOnly))
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	proc.Group(cmd)

	err = cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("agent %s timed out after %s", c.name, c.timeout)
	}
	if err != nil {
		return out, fmt.Errorf("agent %s: %w: %s", c.name, err, Tail(stderr.String(), 2000))
	}
	if out == "" {
		return "", fmt.Errorf("agent %s returned no output: %s", c.name, Tail(stderr.String(), 2000))
	}
	return out, nil
}

func envList(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(m))
	for _, k := range keys {
		out = append(out, k+"="+os.ExpandEnv(m[k]))
	}
	return out
}

// Tail returns at most the last n bytes of s.
func Tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
