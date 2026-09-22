package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dylan-demolder/factory-/internal/state"
)

// Finish writes REPORT.md, commits it, and sends the completion notice.
func (e *Engine) Finish() error {
	p := e.P
	done, blocked, total := p.Counts()
	satisfied := 0
	for _, u := range p.Spec.UseCases {
		if u.Verdict == "satisfied" {
			satisfied++
		}
	}
	status := "complete"
	if blocked > 0 || satisfied < len(p.Spec.UseCases) || done < total {
		status = "finished-with-issues"
	}
	p.Outcome = status
	if err := os.WriteFile(filepath.Join(e.root(), "REPORT.md"), []byte(Report(p)), 0o644); err != nil {
		return err
	}
	if _, err := e.commit("factory: build report"); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s: %d/%d tasks done, %d blocked, %d/%d use cases satisfied", p.Name, done, total, blocked, satisfied, len(p.Spec.UseCases))
	e.logf("■ %s — %s", status, msg)
	e.notify(status, msg)
	return e.save()
}

// Report renders the human-readable build report.
func Report(p *state.Project) string {
	var b strings.Builder
	done, blocked, total := p.Counts()
	fmt.Fprintf(&b, "# Build report: %s\n\n", p.Name)
	fmt.Fprintf(&b, "- Outcome: **%s**\n- Generated: %s\n- Tasks: %d/%d done, %d blocked\n- Acceptance rounds: %d\n- Test command: `%s`\n\n",
		p.Outcome, time.Now().Format(time.RFC1123), done, total, blocked, p.AcceptanceRound, p.TestCommand)

	b.WriteString("## Use cases\n\n| ID | Goal | Verdict | Gaps |\n|---|---|---|---|\n")
	for _, u := range p.Spec.UseCases {
		verdict := u.Verdict
		if verdict == "" {
			verdict = "not judged"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", u.ID, cell(u.Goal), verdict, cell(strings.Join(u.Gaps, "; ")))
	}

	b.WriteString("\n## Tasks\n\n| ID | Title | Status | Attempts | Commit |\n|---|---|---|---|---|\n")
	for _, t := range p.Tasks {
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %s |\n", t.ID, cell(t.Title), t.Status, t.Attempts, t.Commit)
	}

	var issues []string
	for _, t := range p.Tasks {
		if t.Status == state.TaskBlocked {
			issues = append(issues, fmt.Sprintf("### %s: %s\n%s\nUnfinished work (if any): `.factory/tasks/%s-unfinished.patch`\n", t.ID, t.Title, strings.Join(t.Notes, "\n"), t.ID))
		}
	}
	if len(issues) > 0 {
		b.WriteString("\n## Blocked tasks\n\n" + strings.Join(issues, "\n"))
	}
	b.WriteString("\n## Where to look\n\n- `SPEC.md` — the approved specification\n- `.factory/roundtables/` — every roundtable transcript\n- `.factory/tasks/` — briefs, builder summaries, test logs and reviews per attempt\n- `.factory/acceptance/` — hands-on trial reports and verdicts\n")
	return b.String()
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
