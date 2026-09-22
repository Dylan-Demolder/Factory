package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/extract"
	"github.com/dylan-demolder/factory/internal/state"
)

const fallbackTestCommand = "sh scripts/test.sh"

type planReply struct {
	TestCommand string        `json:"test_command"`
	Tasks       []*state.Task `json:"tasks"`
}

// Plan turns the approved spec into tasks, optionally refined by a roundtable.
func (e *Engine) Plan(ctx context.Context) error {
	p := e.P
	specMD, err := os.ReadFile(filepath.Join(e.root(), "SPEC.md"))
	if err != nil {
		return fmt.Errorf("read SPEC.md: %w", err)
	}
	e.logf("planning tasks")
	e.writeOpencodeConfig()

	var plan planReply
	if _, err := e.callJSON(ctx, e.Cfg.Roles.Planner, agent.Request{
		Stage: "plan", System: plannerSystem(), Prompt: planPrompt(p, string(specMD)), ReadOnly: true,
	}, &plan); err != nil {
		return err
	}
	if len(plan.Tasks) == 0 {
		return errors.New("planner returned no tasks")
	}

	if e.Cfg.Roundtable.PlanEnabled() {
		material := specContext(p) + "\n## Proposed plan\n" + fence("json", mustJSON(plan))
		out, err := e.Roundtable(ctx, "plan", planRoundtableTopic, material, e.Cfg.Roles.Planner, planSynthesis())
		if err != nil {
			return err
		}
		var revised planReply
		if perr := extract.JSON(out, &revised); perr == nil && len(revised.Tasks) > 0 {
			plan = revised
		} else {
			e.logf("plan roundtable output unusable; keeping the original plan")
		}
	}
	p.Tasks = normalizeTasks(plan.Tasks, "T")
	p.TestCommand = firstNonEmpty(e.Cfg.TestCommand, plan.TestCommand, p.Spec.TestCommand, fallbackTestCommand)
	e.Store.Write("plan.json", mustJSON(p.Tasks))
	e.logf("plan: %d tasks, test command `%s`", len(p.Tasks), p.TestCommand)
	for _, t := range p.Tasks {
		e.logf("  %s [%s] %s", t.ID, t.Kind, t.Title)
	}
	return nil
}

// normalizeTasks fills defaults, assigns missing/duplicate ids and drops
// dependencies on unknown tasks.
func normalizeTasks(tasks []*state.Task, prefix string) []*state.Task {
	seen := map[string]bool{}
	var out []*state.Task
	for i, t := range tasks {
		if t == nil || strings.TrimSpace(t.Title) == "" {
			continue
		}
		t.ID = strings.TrimSpace(t.ID)
		if t.ID == "" || seen[t.ID] {
			t.ID = fmt.Sprintf("%s%d", prefix, i+1)
			for seen[t.ID] {
				t.ID += "b"
			}
		}
		seen[t.ID] = true
		if t.Kind == "" {
			t.Kind = "feature"
		}
		t.Status = state.TaskPending
		t.Attempts = 0
		out = append(out, t)
	}
	for _, t := range out {
		var deps []string
		for _, d := range t.DependsOn {
			if seen[d] && d != t.ID {
				deps = append(deps, d)
			}
		}
		t.DependsOn = deps
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// writeOpencodeConfig lets opencode edit files and run commands without
// prompting, which an unattended run requires. Existing configs are left alone.
func (e *Engine) writeOpencodeConfig() {
	b, ok := e.Cfg.Agents[e.Cfg.Roles.Builder]
	if !ok || b.Type != "opencode" {
		return
	}
	for _, name := range []string{"opencode.json", "opencode.jsonc"} {
		if _, err := os.Stat(filepath.Join(e.root(), name)); err == nil {
			return
		}
	}
	cfg := `{
  "$schema": "https://opencode.ai/config.json",
  "permission": {
    "edit": "allow",
    "bash": "allow",
    "webfetch": "allow"
  }
}
`
	if err := os.WriteFile(filepath.Join(e.root(), "opencode.json"), []byte(cfg), 0o644); err != nil {
		e.logf("could not write opencode.json: %v", err)
	}
}
