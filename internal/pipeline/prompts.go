package pipeline

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dylan-demolder/factory/internal/state"
)

const autonomyNote = "The project will be built by autonomous AI coding agents with no human available after the spec is approved, so everything must be concrete, unambiguous and testable."

const specFormat = `Write the specification as Markdown with these sections:
# <Project name>
## Summary
## Users
## Use cases — each with an ID (UC1, UC2, …), actor, goal, a step-by-step scenario with concrete inputs, and observable success criteria.
## Features — each with an ID (F1, F2, …), description, and testable acceptance criteria.
## Non-goals
## Technical approach — language, stack, structure, how to run it, and ONE shell command that runs the full automated test suite.
## Quality bar & testing strategy
## Risks & assumptions

Use cases must be genuine: things a real user would actually do, end to end, with realistic inputs and observable outcomes — not restatements of features. Prefer a small product that fully works over a large one that half works.

After the Markdown, output exactly one fenced ` + "```json" + ` block:
{"summary":"...","features":[{"id":"F1","title":"...","description":"...","acceptance":["..."]}],"use_cases":[{"id":"UC1","actor":"...","goal":"...","scenario":"...","success":"..."}],"non_goals":["..."],"stack":"...","test_command":"...","open_questions":["..."]}
open_questions: only questions whose answer would materially change the build and that you cannot sensibly default. Usually empty.`

const taskSchema = `{"id":"T1","title":"...","kind":"setup|feature|usecase|fix","description":"...","acceptance":["..."],"depends_on":["..."],"use_cases":["UC1"]}`

func transcript(qa []state.QA) string {
	if len(qa) == 0 {
		return "(none yet)"
	}
	var b strings.Builder
	for i, x := range qa {
		fmt.Fprintf(&b, "Q%d: %s\nA%d: %s\n\n", i+1, x.Q, i+1, x.A)
	}
	return strings.TrimSpace(b.String())
}

func specContext(p *state.Project) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\nSummary: %s\nStack: %s\n\nFeatures:\n", p.Name, p.Spec.Summary, p.Spec.Stack)
	for _, f := range p.Spec.Features {
		fmt.Fprintf(&b, "- %s %s: %s\n", f.ID, f.Title, f.Description)
		for _, a := range f.Acceptance {
			fmt.Fprintf(&b, "    • %s\n", a)
		}
	}
	b.WriteString("\nUse cases:\n")
	b.WriteString(useCaseList(p.Spec.UseCases))
	if len(p.Spec.NonGoals) > 0 {
		b.WriteString("\nNon-goals: " + strings.Join(p.Spec.NonGoals, "; ") + "\n")
	}
	return b.String()
}

func useCaseList(ucs []state.UseCase) string {
	var b strings.Builder
	for _, u := range ucs {
		fmt.Fprintf(&b, "- %s (%s): %s\n    Scenario: %s\n    Success: %s\n", u.ID, u.Actor, u.Goal, u.Scenario, u.Success)
	}
	return b.String()
}

func bullets(items []string) string {
	if len(items) == 0 {
		return "- (none)\n"
	}
	var b strings.Builder
	for _, s := range items {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	return b.String()
}

func taskBlock(t *state.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s: %s (%s)\n%s\n\nAcceptance criteria:\n%s", t.ID, t.Title, t.Kind, t.Description, bullets(t.Acceptance))
	if len(t.UseCases) > 0 {
		fmt.Fprintf(&b, "Serves use cases: %s\n", strings.Join(t.UseCases, ", "))
	}
	return b.String()
}

func doneTasks(p *state.Project) string {
	var b strings.Builder
	for _, t := range p.Tasks {
		if t.Status == state.TaskDone {
			fmt.Fprintf(&b, "- %s %s\n", t.ID, t.Title)
		}
	}
	if b.Len() == 0 {
		return "- (nothing yet)\n"
	}
	return b.String()
}

// ---- spec ----

func interviewSystem() string {
	return "You are a senior product engineer helping a user turn a project idea into a buildable specification. " + autonomyNote
}

func interviewPrompt(p *state.Project, round, maxRounds int) string {
	return fmt.Sprintf(`Project name: %s

Idea:
%s

Interview so far:
%s

This is question round %d of at most %d. Ask the next batch of clarifying questions (at most 6) that most reduce ambiguity for an autonomous build. Focus on: who the real users are and what they genuinely need to do end to end; core features versus non-goals; platform, stack and deployment constraints; data, integrations and external services (and credentials the agents won't have); quality bar; how success is judged.
Don't ask things you can sensibly decide yourself. Where useful, include your proposed default in the question so the user can simply accept it.
If you already have enough to write a complete spec, set "ready": true and return no questions.

Respond with ONLY JSON: {"questions":["..."],"ready":false}`, p.Name, p.Idea, transcript(p.Interview), round, maxRounds)
}

func draftSpecPrompt(p *state.Project) string {
	return fmt.Sprintf(`Project name: %s

Idea:
%s

Interview:
%s

Write the complete specification. Where the user gave no preference, choose sensible, simple defaults and state them under Risks & assumptions.

%s`, p.Name, p.Idea, transcript(p.Interview), specFormat)
}

func reviseSpecPrompt(p *state.Project, current, feedback string) string {
	return fmt.Sprintf(`Project name: %s

Interview:
%s

Current specification:
%s

Revise the specification based on this input (the user's answers take precedence over everything else):
%s

Output the full revised specification.

%s`, p.Name, transcript(p.Interview), current, feedback, specFormat)
}

const specRoundtableTopic = `Review this draft specification before it goes to autonomous builders. From your perspective: Which genuine use cases are missing, unrealistic, or not really end to end? What is ambiguous or untestable? What should be cut so the product actually ships working? What will likely go wrong during an unattended build (missing credentials, external services, platform issues)?`

func specSynthesis(p *state.Project) string {
	return fmt.Sprintf(`You are moderating. Revise the specification, adopting the roundtable's strongest points and deciding any disagreements yourself. The user's interview answers take precedence over the panel.

User interview:
%s

Output the full revised specification.

%s`, transcript(p.Interview), specFormat)
}

// ---- plan ----

func plannerSystem() string {
	return "You are the technical lead planning an autonomous build. " + autonomyNote
}

func planPrompt(p *state.Project, specMD string) string {
	return fmt.Sprintf(`Break the approved spec into an ordered list of implementation tasks for an AI coding agent working in this repository.

Rules:
- T1 (kind "setup") creates the project skeleton, tooling, a README (install/run/test), and a working automated test suite runnable with the test command.
- Each task is one coherent vertical slice that a strong engineer could finish in one focused session, and it must leave the whole test suite green.
- Every task has concrete, verifiable acceptance criteria, and must ship automated tests for them.
- After the features, add one task per use case (kind "usecase") that writes an end-to-end test driving that use case the way a real user would: real entry points, realistic inputs, minimal mocking.
- depends_on lists ids that must be finished first. Keep ids T1, T2, ….
- test_command is one shell command, run from the repository root, that runs the full test suite and exits non-zero on failure.

Specification:
%s

Respond with ONLY JSON: {"test_command":"...","tasks":[%s]}`, specMD, taskSchema)
}

const planRoundtableTopic = `Critique this implementation plan before the autonomous build starts: ordering and dependencies, missing tasks, tasks too large to finish in one pass, vague or unverifiable acceptance criteria, gaps in test coverage, and use cases without a genuine end-to-end test.`

func planSynthesis() string {
	return fmt.Sprintf(`Produce the final plan, adopting the panel's valid points. Respond with ONLY JSON: {"test_command":"...","tasks":[%s]}`, taskSchema)
}

// ---- tasks ----

func taskRoundtableTopic(t *state.Task) string {
	return fmt.Sprintf(`Design discussion for task %s: %s. Agree how to implement it well. Cover the approach and key design decisions, files/modules to touch, edge cases and failure modes, the specific tests that must exist (unit and integration), and how the task serves the real use cases. Be concrete; this is about THIS task, not the whole project.`, t.ID, t.Title)
}

func taskMaterial(p *state.Project, t *state.Task, files string) string {
	return fmt.Sprintf("%s\n## Task\n%s\n## Completed tasks\n%s\n## Repository files\n%s\n", specContext(p), taskBlock(t), doneTasks(p), files)
}

const taskSynthesis = `Write the implementation brief for the builder, in Markdown:
## Approach
## Files
## Edge cases
## Required tests — a checklist of concrete test cases
## Pitfalls
Be concrete and brief (under 500 words). Resolve disagreements; don't list options.`

func builderSystem() string {
	return "You are an expert software engineer working autonomously in this repository. There is no human available: never ask questions, make reasonable decisions and note them."
}

func buildPrompt(p *state.Project, t *state.Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Implement one task of the project %q. The full specification is in SPEC.md at the repository root; read it for context.\n\n", p.Name)
	fmt.Fprintf(&b, "## Already completed\n%s\n", doneTasks(p))
	fmt.Fprintf(&b, "## Your task\n%s\n", taskBlock(t))
	if t.Brief != "" {
		fmt.Fprintf(&b, "## Implementation brief (agreed by the design roundtable)\n%s\n\n", t.Brief)
	}
	if t.LastFeedback != "" {
		fmt.Fprintf(&b, "## Your previous attempt was rejected — fix these first\n%s\n\n", t.LastFeedback)
	}
	fmt.Fprintf(&b, `## Requirements
- Write real automated tests for every acceptance criterion and every required test in the brief. Tests must exercise real behaviour: no placeholder or tautological assertions, no skipped tests, don't mock the unit under test.
- The entire test suite must pass with: %s
  Run it yourself and keep iterating until it is green.
- Keep README.md accurate (install, run, test).
- Never modify or delete anything under .factory/. Do not run git commit; factory commits for you.
- If something genuinely cannot be done (e.g. needs credentials), implement everything else, make the code degrade gracefully, and explain.

When finished, reply with a short summary of what you changed and any decisions you made.`, "`"+p.TestCommand+"`")
	return b.String()
}

func reviewerSystem() string {
	return "You are a strict, fair QA lead reviewing work produced by an autonomous coding agent. You only pass work you would ship."
}

func reviewPrompt(p *state.Project, t *state.Task, tr testResult, stat, diff string) string {
	brief := t.Brief
	if brief == "" {
		brief = "(no brief)"
	}
	return fmt.Sprintf(`Decide whether this task is genuinely complete.

%s
## Implementation brief
%s

## Test run: %s (exit %d)
%s

## Changes
%s

%s

Fail the task if: any acceptance criterion is not implemented; tests for a criterion or required test case are missing; tests are superficial (tautological asserts, mocking the thing under test, skipped or commented-out tests, tests that can't fail); the change contradicts SPEC.md; or it introduces obvious bugs or security holes. Otherwise pass — don't fail for style nits.

Respond with ONLY JSON: {"verdict":"pass" or "fail","issues":["specific, actionable problem"],"summary":"one sentence"}`,
		taskBlock(t), brief, "`"+p.TestCommand+"`", tr.ExitCode, tr.Output, stat, fence("diff", diff))
}

func fence(lang, s string) string {
	return "```" + lang + "\n" + strings.TrimSpace(s) + "\n```"
}

// ---- acceptance ----

func trialPrompt(p *state.Project) string {
	return fmt.Sprintf(`The build of %q is complete. Act as its real users and actually use it.

For each use case below, follow the scenario end to end through the real entry points (run the CLI, start the server and send real requests, drive the UI headlessly, etc.) with realistic inputs. Use a temporary directory for scratch data, don't modify tracked source files, and stop anything you start. Report honestly — a use case "worked" only if you saw it work.

Use cases:
%s
End your reply with ONLY this JSON in a fenced block:
{"results":[{"id":"UC1","worked":true,"what_i_did":"commands you ran","observations":"what happened; anything broken, confusing or missing"}]}`, p.Name, useCaseList(p.Spec.UseCases))
}

const acceptanceTopic = `Acceptance review. Judge — as the real users named in each use case — whether this software genuinely delivers each use case. Evidence: the hands-on trial report, the test results, and the repository. Be skeptical: green tests are not proof if they don't reflect real usage, and a trial that "worked" but only via workarounds is a gap.`

func acceptanceMaterial(p *state.Project, trial string, tr testResult, files, readme string) string {
	return fmt.Sprintf(`%s
## Hands-on trial report
%s

## Test suite: %s (exit %d)
%s

## Repository files
%s

## README.md
%s
`, specContext(p), trial, "`"+p.TestCommand+"`", tr.ExitCode, tr.Output, files, readme)
}

func acceptanceSynthesis() string {
	return `Deliver the verdict. For each use case: "satisfied" is true only if the evidence shows a real user could achieve it; list concrete gaps otherwise. Then list the fix tasks needed to close every gap and to get the test suite green (empty if none). Fix tasks need concrete acceptance criteria and must include tests.

Respond with ONLY JSON: {"use_cases":[{"id":"UC1","satisfied":true,"gaps":["..."]}],"fix_tasks":[{"title":"...","description":"...","acceptance":["..."],"use_cases":["UC1"]}]}`
}

func mustJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
