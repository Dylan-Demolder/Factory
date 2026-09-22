package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/extract"
	"github.com/dylan-demolder/factory/internal/state"
)

type acceptanceVerdict struct {
	UseCases []struct {
		ID        string   `json:"id"`
		Satisfied bool     `json:"satisfied"`
		Gaps      []string `json:"gaps"`
	} `json:"use_cases"`
	FixTasks []*state.Task `json:"fix_tasks"`
}

// Accept checks the finished build against the genuine use cases:
//  1. the builder agent actually uses the software for each use case,
//  2. the full test suite runs,
//  3. the roundtable judges each use case as its real users,
//
// and any gaps become fix tasks. It reports whether everything passed.
func (e *Engine) Accept(ctx context.Context) (bool, error) {
	p := e.P
	round := p.AcceptanceRound + 1
	e.logf("acceptance round %d/%d", round, e.Cfg.Limits.MaxAcceptanceRounds)

	trial := "(no hands-on trial: the spec has no use cases)"
	if len(p.Spec.UseCases) > 0 {
		e.logf("  hands-on trial of %d use case(s) by %s", len(p.Spec.UseCases), e.roleLabel(config.RoleBuilder))
		out, err := e.callRole(ctx, config.RoleBuilder, agent.Request{Stage: "trial", System: builderSystem(), Prompt: trialPrompt(p)})
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			trial = "(trial failed to run: " + err.Error() + ")"
		} else {
			trial = out
			var parsed struct {
				Results []map[string]any `json:"results"`
			}
			if extract.JSON(out, &parsed) == nil {
				trial = fence("json", mustJSON(parsed))
			}
		}
		e.Store.Write(fmt.Sprintf("acceptance/round-%d-trial.md", round), trial)
		// The trial must not leave changes behind; anything it touched is discarded.
		if stat, _ := e.stagedDiff(1); strings.TrimSpace(stat) != "" {
			e.logf("  trial modified files; discarding those changes")
			if err := e.discard(fmt.Sprintf("acceptance/round-%d-trial.patch", round)); err != nil {
				return false, err
			}
		}
	}

	tr := e.runTests(ctx)
	e.Store.Write(fmt.Sprintf("acceptance/round-%d-tests.log", round), tr.Output)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	readme, _ := os.ReadFile(filepath.Join(e.root(), "README.md"))
	material := acceptanceMaterial(p, trial, tr, e.files(300), agent.Tail(string(readme), 8000))

	out, err := e.Roundtable(ctx, fmt.Sprintf("acceptance-%d", round), acceptanceTopic, material, config.RoleModerator, acceptanceSynthesis())
	if err != nil {
		return false, err
	}
	var v acceptanceVerdict
	if err := extract.JSON(out, &v); err != nil {
		// Ask the moderator to restate the verdict as JSON.
		if _, err := e.callRoleJSON(ctx, config.RoleModerator, agent.Request{
			Stage: "acceptance-verdict", ReadOnly: true,
			Prompt: "Restate this acceptance verdict in the required JSON format.\n\n" + out + "\n\n" + acceptanceSynthesis(),
		}, &v); err != nil {
			return false, err
		}
	}
	e.Store.Write(fmt.Sprintf("acceptance/round-%d-verdict.json", round), mustJSON(v))

	verdicts := map[string]int{}
	for i, u := range v.UseCases {
		verdicts[strings.TrimSpace(u.ID)] = i
	}
	allOK := tr.Passed
	for i := range p.Spec.UseCases {
		uc := &p.Spec.UseCases[i]
		idx, ok := verdicts[uc.ID]
		switch {
		case !ok:
			uc.Verdict, uc.Gaps = "unsatisfied", []string{"no verdict given by the acceptance roundtable"}
		case v.UseCases[idx].Satisfied:
			uc.Verdict, uc.Gaps = "satisfied", nil
		default:
			uc.Verdict, uc.Gaps = "unsatisfied", v.UseCases[idx].Gaps
		}
		if uc.Verdict != "satisfied" {
			allOK = false
		}
		e.logf("  %s %s: %s", uc.ID, uc.Goal, uc.Verdict)
	}
	if !tr.Passed {
		e.logf("  test suite is failing (exit %d)", tr.ExitCode)
	}
	if allOK {
		e.logf("✔ acceptance passed")
		return true, nil
	}
	if round >= e.Cfg.Limits.MaxAcceptanceRounds {
		e.logf("  last acceptance round: remaining gaps go in the report")
		return false, nil
	}

	fixes := v.FixTasks
	if len(fixes) == 0 {
		// The panel found problems but proposed no fixes; derive them.
		for _, uc := range p.Spec.UseCases {
			if uc.Verdict != "satisfied" {
				fixes = append(fixes, &state.Task{
					Title:       "Make " + uc.ID + " genuinely work: " + uc.Goal,
					Description: "The acceptance review found this use case is not delivered. Scenario: " + uc.Scenario,
					Acceptance:  append(append([]string{}, uc.Gaps...), uc.Success, "An end-to-end test drives this scenario and passes"),
					UseCases:    []string{uc.ID},
				})
			}
		}
		if !tr.Passed {
			fixes = append(fixes, &state.Task{
				Title:       "Make the full test suite pass",
				Description: "The test suite fails at acceptance. Output tail:\n" + tr.Output,
				Acceptance:  []string{"`" + p.TestCommand + "` exits 0 without skipping or weakening tests"},
			})
		}
	}
	for i, t := range fixes {
		if t == nil {
			continue
		}
		t.ID = fmt.Sprintf("A%d.%d", round, i+1)
		t.Kind = "fix"
		t.DependsOn = nil
	}
	fixes = normalizeTasks(fixes, fmt.Sprintf("A%d.", round))
	p.Tasks = append(p.Tasks, fixes...)
	e.logf("  %d fix task(s) added", len(fixes))
	return false, nil
}
