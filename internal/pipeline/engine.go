// Package pipeline runs a project from idea to accepted software:
//
//	interview → spec (+roundtable) → approval            (human in the loop)
//	plan (+roundtable) → per task: design roundtable → build → test → review
//	→ hands-on use-case trial → acceptance roundtable → fix tasks → …      (autonomous)
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/extract"
	"github.com/dylan-demolder/factory/internal/state"
	"github.com/dylan-demolder/factory/internal/ui"
)

type Engine struct {
	Cfg    *config.Config
	Agents map[string]agent.Agent
	Store  *state.Store
	P      *state.Project
	Out    io.Writer
	UI     ui.Asker // nil when running unattended
	// Backoff between agent retries.
	Backoff time.Duration

	// Seats resolve lazily and are cached. A seat with a role_models
	// override needs its own agent object — the shared one in Agents runs
	// the agent's own model — so caching keeps one object per seat rather
	// than rebuilding on every call.
	roleMu    sync.Mutex
	roleCache map[string]roleSeat
}

func New(cfg *config.Config, agents map[string]agent.Agent, store *state.Store, p *state.Project, out io.Writer) *Engine {
	return &Engine{Cfg: cfg, Agents: agents, Store: store, P: p, Out: out, Backoff: 10 * time.Second}
}

func (e *Engine) root() string { return e.Store.Root }

func (e *Engine) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(e.Out, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
	e.Store.Event("log", msg)
}

func (e *Engine) save() error { return e.Store.Save(e.P) }

func (e *Engine) agent(name string) (agent.Agent, error) {
	a, ok := e.Agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not configured", name)
	}
	return a, nil
}

// roleSeat is a pipeline seat resolved to a running agent, plus the label
// logs and events should use for it.
type roleSeat struct {
	agent agent.Agent
	label string
}

// roleConfig is a seat's agent config with that seat's model override
// applied. Seats are separate from agents: one agent can hold several seats,
// and role_models lets those seats run different models.
func (e *Engine) roleConfig(role string) (string, config.Agent, error) {
	name := e.Cfg.Role(role)
	if name == "" {
		return "", config.Agent{}, fmt.Errorf("role %s: no agent assigned", role)
	}
	base, ok := e.Cfg.Agents[name]
	if !ok {
		return name, config.Agent{}, fmt.Errorf("agent %q not configured", name)
	}
	if m := e.Cfg.RoleModel(role); m != "" {
		base.Model = m
	}
	return name, base, nil
}

// seat resolves a role, applying its model override, and caches the result.
//
// When no override applies, the agent already built in e.Agents is reused as
// is. That matters beyond efficiency: e.Agents is where callers inject
// substitutes (the test suite supplies in-memory fakes), and rebuilding from
// config would quietly launch the real binary instead.
func (e *Engine) seat(role string) (roleSeat, error) {
	e.roleMu.Lock()
	defer e.roleMu.Unlock()
	if s, ok := e.roleCache[role]; ok {
		return s, nil
	}
	name, cfg, err := e.roleConfig(role)
	if err != nil {
		return roleSeat{}, err
	}
	s := roleSeat{label: name}
	override := e.Cfg.RoleModel(role)
	if override == "" {
		// No override for this seat: the shared agent is exactly right.
		if a, ok := e.Agents[name]; ok {
			s.agent = a
			e.roleCacheSet(role, s)
			return s, nil
		}
	}
	if orig, ok := e.Cfg.Agents[name]; ok && cfg.Model != orig.Model {
		// Show the override in the label: "oc-qa (opencode-go/kimi-k3)"
		s.label = fmt.Sprintf("%s (%s)", name, cfg.Model)
	}
	a, err := agent.New(name, cfg, e.Cfg.Limits.AgentTimeout.Duration)
	if err != nil {
		return roleSeat{}, err
	}
	s.agent = a
	e.roleCacheSet(role, s)
	return s, nil
}

// roleCacheSet stores a resolved seat; the caller holds roleMu.
func (e *Engine) roleCacheSet(role string, s roleSeat) {
	if e.roleCache == nil {
		e.roleCache = map[string]roleSeat{}
	}
	e.roleCache[role] = s
}

// roleLabel describes a seat for humans: its agent, plus the model when a
// role_models override changes it.
func (e *Engine) roleLabel(role string) string {
	if s, err := e.seat(role); err == nil {
		return s.label
	}
	if name := e.Cfg.Role(role); name != "" {
		return name
	}
	return role
}

// call runs a named agent — used for roundtable seats, which are agents
// rather than pipeline roles — with retries on failure.
func (e *Engine) call(ctx context.Context, name string, req agent.Request) (string, error) {
	a, err := e.agent(name)
	if err != nil {
		return "", err
	}
	return e.callWith(ctx, name, a, req)
}

// callRole runs the agent filling a pipeline seat, applying that seat's model
// override. Every seat goes through here so the override lives in one place.
func (e *Engine) callRole(ctx context.Context, role string, req agent.Request) (string, error) {
	s, err := e.seat(role)
	if err != nil {
		return "", err
	}
	return e.callWith(ctx, s.label, s.agent, req)
}

func (e *Engine) callWith(ctx context.Context, label string, a agent.Agent, req agent.Request) (string, error) {
	if req.Dir == "" {
		req.Dir = e.root()
	}
	var lastErr error
	for attempt := 0; attempt <= e.Cfg.Limits.AgentRetries; attempt++ {
		if attempt > 0 {
			e.logf("  retrying %s (%s) after error: %v", label, req.Stage, lastErr)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(e.Backoff * time.Duration(attempt)):
			}
		}
		start := time.Now()
		out, err := a.Run(ctx, req)
		if err == nil {
			e.Store.Event("agent", fmt.Sprintf("%s %s ok in %s (%d chars)", label, req.Stage, time.Since(start).Round(time.Second), len(out)))
			return out, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		lastErr = err
	}
	return "", lastErr
}

// callJSON calls a named agent and decodes a JSON answer into v, asking once
// more for a corrected answer if parsing fails.
func (e *Engine) callJSON(ctx context.Context, name string, req agent.Request, v any) (string, error) {
	a, err := e.agent(name)
	if err != nil {
		return "", err
	}
	return e.callJSONWith(ctx, name, a, req, v)
}

// callRoleJSON is callJSON for a pipeline seat, applying its model override.
func (e *Engine) callRoleJSON(ctx context.Context, role string, req agent.Request, v any) (string, error) {
	s, err := e.seat(role)
	if err != nil {
		return "", err
	}
	return e.callJSONWith(ctx, s.label, s.agent, req, v)
}

func (e *Engine) callJSONWith(ctx context.Context, label string, a agent.Agent, req agent.Request, v any) (string, error) {
	out, err := e.callWith(ctx, label, a, req)
	if err != nil {
		return "", err
	}
	perr := extract.JSON(out, v)
	if perr == nil {
		return out, nil
	}
	e.logf("  %s gave unparseable output for %s (%v); asking again", label, req.Stage, perr)
	retry := req
	retry.Stage = req.Stage + "-repair"
	retry.Prompt = req.Prompt + "\n\n---\nYour previous reply could not be parsed as JSON (" + perr.Error() +
		"). Reply again with ONLY the JSON object in the required schema, in a ```json fenced block, and nothing else.\n\nPrevious reply:\n" + agent.Tail(out, 6000)
	out, err = e.callWith(ctx, label, a, retry)
	if err != nil {
		return "", err
	}
	if err := extract.JSON(out, v); err != nil {
		return out, fmt.Errorf("%s: %s output not parseable: %w", label, req.Stage, err)
	}
	return out, nil
}

// notify runs the configured notify_command, if any.
func (e *Engine) notify(status, message string) {
	if e.Cfg.NotifyCommand == "" {
		return
	}
	cmd := exec.Command("sh", "-c", e.Cfg.NotifyCommand)
	cmd.Dir = e.root()
	cmd.Env = append(os.Environ(),
		"FACTORY_PROJECT="+e.P.Name,
		"FACTORY_STATUS="+status,
		"FACTORY_MESSAGE="+message,
		"FACTORY_DIR="+e.root(),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		e.logf("notify_command failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// Run drives the autonomous part of the pipeline until done. It is safe to
// call again after an interruption: work resumes from saved state.
func (e *Engine) Run(ctx context.Context) error {
	p := e.P
	for _, t := range p.Tasks {
		if t.Status == state.TaskActive {
			t.Status = state.TaskPending
		}
	}
	if err := e.ensureRepo(); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			e.save()
			return err
		}
		var err error
		switch p.Phase {
		case state.PhaseSpec, "":
			return errors.New("the spec has not been approved yet; run `factory spec` first")
		case state.PhaseSpecified:
			if err = e.Plan(ctx); err == nil {
				p.Phase = state.PhaseBuilding
			}
		case state.PhaseBuilding:
			if err = e.BuildAll(ctx); err == nil {
				p.Phase = state.PhaseAccepting
			}
		case state.PhaseAccepting:
			if p.AcceptanceRound >= e.Cfg.Limits.MaxAcceptanceRounds {
				e.logf("acceptance round limit (%d) reached", e.Cfg.Limits.MaxAcceptanceRounds)
				p.Phase = state.PhaseDone
				break
			}
			var ok bool
			ok, err = e.Accept(ctx)
			if err == nil {
				p.AcceptanceRound++
				if ok || p.AcceptanceRound >= e.Cfg.Limits.MaxAcceptanceRounds {
					p.Phase = state.PhaseDone
				} else {
					p.Phase = state.PhaseBuilding
				}
			}
		case state.PhaseDone:
			return e.Finish()
		default:
			return fmt.Errorf("unknown phase %q", p.Phase)
		}
		if serr := e.save(); serr != nil {
			return serr
		}
		if err != nil {
			if ctx.Err() == nil {
				e.notify("error", err.Error())
			}
			return err
		}
	}
}
