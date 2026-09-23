package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/proc"
	"github.com/dylan-demolder/factory/internal/state"
)

const (
	testOutputLimit = 12 * 1024
	diffLimit       = 40 * 1024
)

type testResult struct {
	Passed   bool
	ExitCode int
	Output   string
	Duration time.Duration
}

// BuildAll works through every runnable task until none are left.
func (e *Engine) BuildAll(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		t := e.nextTask()
		if t == nil {
			done, blocked, total := e.P.Counts()
			e.logf("build pass finished: %d/%d done, %d blocked", done, total, blocked)
			return nil
		}
		if err := e.runTask(ctx, t); err != nil {
			return err
		}
	}
}

// nextTask returns the first pending task whose dependencies are done,
// blocking tasks whose dependencies are blocked. If only tasks with
// unsatisfiable (e.g. cyclic) dependencies remain, it returns the first
// of them anyway: planner dependencies are advice, not law.
func (e *Engine) nextTask() *state.Task {
	p := e.P
	for changed := true; changed; {
		changed = false
		for _, t := range p.Tasks {
			if t.Status != state.TaskPending {
				continue
			}
			for _, d := range t.DependsOn {
				if dt := p.Task(d); dt != nil && dt.Status == state.TaskBlocked {
					t.Status = state.TaskBlocked
					t.Notes = append(t.Notes, "blocked because dependency "+d+" is blocked")
					e.logf("%s blocked: dependency %s is blocked", t.ID, d)
					changed = true
					break
				}
			}
		}
	}
	var fallback *state.Task
	for _, t := range p.Tasks {
		if t.Status != state.TaskPending {
			continue
		}
		if fallback == nil {
			fallback = t
		}
		ready := true
		for _, d := range t.DependsOn {
			if dt := p.Task(d); dt != nil && dt.Status != state.TaskDone {
				ready = false
				break
			}
		}
		if ready {
			return t
		}
	}
	return fallback
}

func (e *Engine) runTask(ctx context.Context, t *state.Task) error {
	cfg := e.Cfg
	t.Status = state.TaskActive
	e.save()
	e.logf("▶ %s: %s", t.ID, t.Title)

	if t.Brief == "" && cfg.Roundtable.TasksEnabled() {
		brief, err := e.Roundtable(ctx, "task-"+t.ID, taskRoundtableTopic(t), taskMaterial(e.P, t, e.files(300)), config.RoleModerator, taskSynthesis)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.logf("  design roundtable failed (%v); building without a brief", err)
		} else {
			t.Brief = strings.TrimSpace(brief)
			e.Store.Write("tasks/"+t.ID+"-brief.md", t.Brief)
		}
		e.save()
	}

	for t.Attempts < cfg.Limits.MaxTaskAttempts {
		// A failure that repeats points at the brief, not at the builder:
		// hand the design roundtable the history and let it correct the
		// approach once, rather than spending the remaining attempts on the
		// same instruction. Bounded to one re-brief per task.
		if cfg.Roundtable.TasksEnabled() && t.Rebriefs == 0 {
			if sig, n := mostRepeated(t.Failures); n >= 2 {
				e.logf("  %s failed identically %d times (%s); re-running the design roundtable", t.ID, n, sig)
				material := taskMaterial(e.P, t, e.files(300)) +
					"\n\n## Failure history — the previous brief led here\n" + failureHistory(t) +
					"\nThe previous brief may itself be wrong. If its suggested code cannot compile " +
					"or fit this codebase, say so explicitly and give a corrected approach.\n"
				reBrief, err := e.Roundtable(ctx, "task-"+t.ID+"-rebrief", taskRoundtableTopic(t),
					material, config.RoleModerator, taskSynthesis)
				if err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					e.logf("  re-brief failed (%v); continuing with the existing brief", err)
				} else if strings.TrimSpace(reBrief) != "" {
					t.Brief = strings.TrimSpace(reBrief)
					t.Rebriefs++
					t.LastFeedback = "" // the corrected brief supersedes it
					e.Store.Write(fmt.Sprintf("tasks/%s-brief-re%d.md", t.ID, t.Rebriefs), t.Brief)
					e.save()
				}
			}
		}

		t.Attempts++
		e.save()
		e.logf("  attempt %d/%d: building with %s", t.Attempts, cfg.Limits.MaxTaskAttempts, e.roleLabel(config.RoleBuilder))

		summary, err := e.callRole(ctx, config.RoleBuilder, agent.Request{
			Stage: "build", System: builderSystem(), Prompt: buildPrompt(e.P, t),
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sig := failureSignature("error: " + err.Error())
			t.Failures = append(t.Failures, state.Failure{Attempt: t.Attempts, Sig: sig})
			t.LastFeedback = repeatNote(t.Failures, sig) +
				"The builder failed to complete: " + err.Error()
			e.logf("  builder error: %v", err)
			e.save()
			continue
		}
		e.Store.Write(fmt.Sprintf("tasks/%s-attempt-%d-builder.md", t.ID, t.Attempts), summary)

		tr := e.runTests(ctx)
		e.Store.Write(fmt.Sprintf("tasks/%s-attempt-%d-tests.log", t.ID, t.Attempts), tr.Output)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !tr.Passed {
			e.logf("  tests failed (exit %d)", tr.ExitCode)
			sig := failureSignature(tr.Output)
			t.Failures = append(t.Failures, state.Failure{Attempt: t.Attempts, Sig: sig})
			// Naming a repeat is what stops a builder from reintroducing an
			// error it already fixed two attempts ago.
			t.LastFeedback = repeatNote(t.Failures, sig) +
				fmt.Sprintf("The test suite failed (`%s`, exit code %d). Output (tail):\n%s",
					e.P.TestCommand, tr.ExitCode, fence("", tr.Output))
			e.save()
			continue
		}
		e.logf("  tests passed in %s; reviewing with %s", tr.Duration.Round(time.Second), e.roleLabel(config.RoleReviewer))

		stat, diff := e.stagedDiff(diffLimit)
		if strings.TrimSpace(stat) == "" {
			stat = "(no file changes)"
		}
		var verdict struct {
			Verdict string   `json:"verdict"`
			Issues  []string `json:"issues"`
			Summary string   `json:"summary"`
		}
		raw, err := e.callRoleJSON(ctx, config.RoleReviewer, agent.Request{
			Stage: "review", System: reviewerSystem(), Prompt: reviewPrompt(e.P, t, tr, stat, diff), ReadOnly: true,
		}, &verdict)
		e.Store.Write(fmt.Sprintf("tasks/%s-attempt-%d-review.md", t.ID, t.Attempts), raw)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.logf("  review failed: %v", err)
			t.LastFeedback = "Tests passed but the review could not be completed; double-check every acceptance criterion has real tests."
			continue
		}
		if strings.EqualFold(strings.TrimSpace(verdict.Verdict), "pass") {
			hash, err := e.commit(fmt.Sprintf("%s: %s\n\n%s", t.ID, t.Title, verdict.Summary))
			if err != nil {
				return err
			}
			t.Status = state.TaskDone
			t.Commit = hash
			t.LastFeedback = ""
			e.logf("✔ %s done (%s) — %s", t.ID, hash, verdict.Summary)
			return e.save()
		}
		e.logf("  review rejected: %s", verdict.Summary)
		t.LastFeedback = "Tests pass, but the reviewer rejected the work:\n" + bullets(verdict.Issues)
	}

	t.Status = state.TaskBlocked
	t.Notes = append(t.Notes, "gave up after "+fmt.Sprint(t.Attempts)+" attempts; last feedback: "+agent.Tail(t.LastFeedback, 1500))
	patch := "tasks/" + t.ID + "-unfinished.patch"
	if err := e.discard(patch); err != nil {
		return err
	}
	e.logf("✖ %s blocked after %d attempts (work saved to .factory/%s)", t.ID, t.Attempts, patch)
	e.notify("blocked", fmt.Sprintf("%s %s is blocked", t.ID, t.Title))
	return e.save()
}

// runTests runs the project's test command.
func (e *Engine) runTests(ctx context.Context) testResult {
	cmdline := e.P.TestCommand
	if cmdline == "" {
		cmdline = fallbackTestCommand
	}
	ctx, cancel := context.WithTimeout(ctx, e.Cfg.Limits.TestTimeout.Duration)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	cmd.Dir = e.root()
	cmd.Env = append(os.Environ(), "CI=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.WaitDelay = 5 * time.Second
	proc.Group(cmd)
	start := time.Now()
	err := cmd.Run()
	res := testResult{Duration: time.Since(start), Output: agent.Tail(out.String(), testOutputLimit)}
	var exitErr *exec.ExitError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.ExitCode = -1
		res.Output += fmt.Sprintf("\n[factory] test command timed out after %s", e.Cfg.Limits.TestTimeout.Duration)
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	case err != nil:
		res.ExitCode = -1
		res.Output += "\n[factory] " + err.Error()
	default:
		res.Passed = true
	}
	if res.Output == "" {
		res.Output = "(no output)"
	}
	return res
}
