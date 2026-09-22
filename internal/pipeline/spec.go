package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dylan-demolder/factory-/internal/agent"
	"github.com/dylan-demolder/factory-/internal/extract"
	"github.com/dylan-demolder/factory-/internal/state"
)

const noPreference = "(no preference — use your best judgement)"

// Spec runs the interactive part: interview, draft, roundtable review,
// open questions, and approval. On success the project is PhaseSpecified.
func (e *Engine) Spec(ctx context.Context) error {
	if e.UI == nil {
		return errors.New("spec needs an interactive terminal")
	}
	p := e.P
	cfg := e.Cfg
	if strings.TrimSpace(p.Idea) == "" {
		idea, err := e.UI.Ask("Describe what you want to build (who it's for, what it should do). Finish with an empty line.")
		if err != nil && err != io.EOF {
			return err
		}
		if strings.TrimSpace(idea) == "" {
			return errors.New("no idea given")
		}
		p.Idea = idea
		e.save()
	}

	// 1. Interview.
	done := false
	for round := 1; round <= cfg.Limits.MaxInterviewRounds && !done; round++ {
		e.UI.Say("\n… thinking about what to ask (round %d)", round)
		var reply struct {
			Questions []string `json:"questions"`
			Ready     bool     `json:"ready"`
		}
		if _, err := e.callJSON(ctx, cfg.Roles.Interviewer, agent.Request{
			Stage: "interview", System: interviewSystem(), Prompt: interviewPrompt(p, round, cfg.Limits.MaxInterviewRounds), ReadOnly: true,
		}, &reply); err != nil {
			return err
		}
		if len(reply.Questions) == 0 {
			break
		}
		e.UI.Say("\nA few questions (empty line = no preference, /done = skip to the spec):")
		for i, q := range reply.Questions {
			ans, err := e.UI.Ask(fmt.Sprintf("[%d/%d] %s", i+1, len(reply.Questions), q), "/done")
			if strings.TrimSpace(ans) == "/done" || (err == io.EOF && ans == "") {
				done = true
				break
			}
			if err != nil && err != io.EOF {
				return err
			}
			if strings.TrimSpace(ans) == "" {
				ans = noPreference
			}
			p.Interview = append(p.Interview, state.QA{Q: q, A: ans})
			e.save()
		}
		if reply.Ready {
			break
		}
	}

	// 2. Draft.
	e.UI.Say("\n… drafting the spec")
	md, data, err := e.specCall(ctx, cfg.Roles.Interviewer, "spec-draft", draftSpecPrompt(p))
	if err != nil {
		return err
	}

	// 3. Roundtable review of the draft.
	if cfg.Roundtable.SpecEnabled() {
		e.UI.Say("… the roundtable is reviewing the draft (use cases, scope, testability)")
		out, err := e.Roundtable(ctx, "spec", specRoundtableTopic, md, cfg.Roles.Moderator, specSynthesis(p))
		if err != nil {
			return err
		}
		if m2, d2, perr := parseSpec(out); perr == nil {
			md, data = m2, d2
		} else {
			e.logf("roundtable synthesis unusable (%v); keeping draft", perr)
		}
	}

	// 4. Open questions + approval loop.
	openRounds := 0
	for {
		if err := e.writeSpec(md, data); err != nil {
			return err
		}
		var feedback []string
		if openRounds >= 2 {
			data.OpenQuestions = nil // don't let the interviewer loop forever
		}
		if len(data.OpenQuestions) > 0 {
			openRounds++
			e.UI.Say("\nThe team has %d open question(s):", len(data.OpenQuestions))
			for _, q := range data.OpenQuestions {
				ans, err := e.UI.Ask(q)
				if err != nil && err != io.EOF {
					return err
				}
				if strings.TrimSpace(ans) == "" {
					ans = noPreference
				}
				p.Interview = append(p.Interview, state.QA{Q: q, A: ans})
				feedback = append(feedback, fmt.Sprintf("Q: %s\nA: %s", q, ans))
			}
		} else {
			e.printSpecSummary(data)
			ans, err := e.UI.Ask("Approve the spec? Type 'y' to approve and hand off, or describe what to change (finish with an empty line).", "y", "yes")
			if err != nil && err != io.EOF {
				return err
			}
			a := strings.ToLower(strings.TrimSpace(ans))
			if a == "y" || a == "yes" {
				break
			}
			if a == "" {
				if err == io.EOF {
					return errors.New("input closed before the spec was approved")
				}
				continue
			}
			p.Interview = append(p.Interview, state.QA{Q: "Change request on the spec", A: ans})
			feedback = append(feedback, ans)
		}
		e.save()
		e.UI.Say("\n… revising the spec")
		m2, d2, err := e.specCall(ctx, cfg.Roles.Interviewer, "spec-revise", reviseSpecPrompt(p, md, strings.Join(feedback, "\n\n")))
		if err != nil {
			return err
		}
		md, data = m2, d2
	}

	data.OpenQuestions = nil
	if err := e.writeSpec(md, data); err != nil {
		return err
	}
	p.Spec = data
	p.Phase = state.PhaseSpecified
	if err := e.ensureRepo(); err != nil {
		return err
	}
	if _, err := e.commit("factory: approved spec"); err != nil {
		return err
	}
	e.logf("spec approved: %d features, %d use cases", len(data.Features), len(data.UseCases))
	return e.save()
}

func (e *Engine) printSpecSummary(d state.SpecData) {
	e.UI.Say("\n══ Spec: %s ══", filepath.Join(e.root(), "SPEC.md"))
	e.UI.Say("%s\n", d.Summary)
	e.UI.Say("Use cases:")
	for _, u := range d.UseCases {
		e.UI.Say("  %s  %s — %s", u.ID, u.Actor, u.Goal)
	}
	e.UI.Say("Features:")
	for _, f := range d.Features {
		e.UI.Say("  %s  %s", f.ID, f.Title)
	}
	if d.Stack != "" {
		e.UI.Say("Stack: %s", d.Stack)
	}
	if d.TestCommand != "" {
		e.UI.Say("Tests: %s", d.TestCommand)
	}
	e.UI.Say("(Read the full spec in SPEC.md before approving.)")
}

func (e *Engine) specCall(ctx context.Context, agentName, stage, prompt string) (string, state.SpecData, error) {
	out, err := e.call(ctx, agentName, agent.Request{Stage: stage, System: interviewSystem(), Prompt: prompt, ReadOnly: true})
	if err != nil {
		return "", state.SpecData{}, err
	}
	md, data, perr := parseSpec(out)
	if perr == nil {
		return md, data, nil
	}
	e.logf("  spec output unparseable (%v); asking again", perr)
	out, err = e.call(ctx, agentName, agent.Request{
		Stage: stage + "-repair", System: interviewSystem(), ReadOnly: true,
		Prompt: prompt + "\n\n---\nYour previous reply was missing a valid ```json block (" + perr.Error() + "). Reply again in the exact required format.",
	})
	if err != nil {
		return "", state.SpecData{}, err
	}
	return parseSpec(out)
}

func parseSpec(out string) (string, state.SpecData, error) {
	var d state.SpecData
	if err := extract.JSON(out, &d); err != nil {
		return "", d, err
	}
	if len(d.UseCases) == 0 && len(d.Features) == 0 {
		return "", d, errors.New("spec JSON has no use cases or features")
	}
	md := extract.BeforeLastJSONFence(out)
	if !strings.Contains(md, "#") {
		md = renderSpec(d)
	}
	return md, d, nil
}

func renderSpec(d state.SpecData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Specification\n\n## Summary\n%s\n\n## Use cases\n", d.Summary)
	for _, u := range d.UseCases {
		fmt.Fprintf(&b, "### %s — %s\n- Actor: %s\n- Scenario: %s\n- Success: %s\n\n", u.ID, u.Goal, u.Actor, u.Scenario, u.Success)
	}
	b.WriteString("## Features\n")
	for _, f := range d.Features {
		fmt.Fprintf(&b, "### %s — %s\n%s\n\nAcceptance:\n%s\n", f.ID, f.Title, f.Description, bullets(f.Acceptance))
	}
	fmt.Fprintf(&b, "## Non-goals\n%s\n## Technical approach\n%s\n\nTest command: `%s`\n", bullets(d.NonGoals), d.Stack, d.TestCommand)
	return b.String()
}

func (e *Engine) writeSpec(md string, d state.SpecData) error {
	if err := os.WriteFile(filepath.Join(e.root(), "SPEC.md"), []byte(md+"\n"), 0o644); err != nil {
		return err
	}
	return e.Store.Write("spec.json", mustJSON(d))
}
