package tui

// The doctor answers one question: does every configured agent actually
// reply? It mirrors internal/web's handleDoctor — resolve, build the agents,
// run them all concurrently against a scratch directory in read-only mode —
// but spread across bubbletea's command goroutines so the UI keeps painting
// while the agents think.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
)

// doctorStartedMsg asks the screen to begin a run. runDoctor yields this
// instead of doing the work itself for two reasons: the root calls runDoctor
// over a copy of the model, so the "running" flag can only be set where the
// message lands — and updateDoctor uses that same landing to guard against a
// double-fire (the command palette batches setRoute with runDoctor, and
// setRoute fires runDoctor again on its own).
type doctorStartedMsg struct{}

// doctorDoneMsg delivers the whole result set at once. The run sorts them by
// name before posting, so the table is stable between draws.
type doctorDoneMsg struct {
	path    string
	results []doctorResult
	at      time.Time
	err     error // setup failure: resolve, NewAll, scratch dir
}

// doctorResult is one agent's round trip.
type doctorResult struct {
	name    string
	ok      bool
	reply   string
	errText string
	took    time.Duration
}

// doctorState is the screen's memory of the last run; results survive
// navigation as long as the program lives.
type doctorState struct {
	running bool
	done    bool
	results []doctorResult
	at      time.Time
	err     error
	path    string
}

// tally is the n/m the summary line is built from.
func (d doctorState) tally() (ok, total int) {
	total = len(d.results)
	for _, r := range d.results {
		if r.ok {
			ok++
		}
	}
	return ok, total
}

// runDoctor requests a check; the work itself is spawned by updateDoctor when
// this message lands (see doctorStartedMsg above).
func (m Model) runDoctor() tea.Cmd {
	return func() tea.Msg { return doctorStartedMsg{} }
}

// doctorWork is the run: resolve the config, build every agent, then ask each
// one — concurrently — for exactly "OK". Each agent gets a private scratch
// directory and a read-only request, and the whole thing a three-minute
// budget, so one wedged agent cannot hold the screen hostage. The reply is
// tailed to its last 120 characters: enough to see what came back without
// carrying a transcript through a message.
func (m Model) doctorWork() tea.Cmd {
	wanted := m.CfgPath
	return func() tea.Msg {
		cfg, path, err := config.Resolve(wanted, "")
		if err != nil {
			return doctorDoneMsg{err: err, at: time.Now()}
		}
		agents, err := agent.NewAll(cfg)
		if err != nil {
			return doctorDoneMsg{path: path, err: err, at: time.Now()}
		}
		dir, err := os.MkdirTemp("", "factory-doctor-")
		if err != nil {
			return doctorDoneMsg{path: path, err: err, at: time.Now()}
		}
		defer os.RemoveAll(dir)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()

		results := make([]doctorResult, 0, len(agents))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for name, a := range agents {
			wg.Add(1)
			go func(name string, a agent.Agent) {
				defer wg.Done()
				start := time.Now()
				out, runErr := a.Run(ctx, agent.Request{
					Stage:    "doctor",
					Prompt:   "Reply with exactly the word OK and nothing else.",
					Dir:      dir,
					ReadOnly: true,
				})
				res := doctorResult{
					name:  name,
					ok:    runErr == nil,
					reply: agent.Tail(out, 120),
					took:  time.Since(start).Round(100 * time.Millisecond),
				}
				if runErr != nil {
					res.errText = runErr.Error()
				}
				mu.Lock()
				results = append(results, res)
				mu.Unlock()
			}(name, a)
		}
		wg.Wait()
		sort.Slice(results, func(i, j int) bool { return results[i].name < results[j].name })
		return doctorDoneMsg{path: path, results: results, at: time.Now()}
	}
}

// updateDoctor owns the run's lifecycle: start once, land once, and say what
// came back. Nothing here blocks — the agents run on the command's goroutine
// and report through doctorDoneMsg.
func (m Model) updateDoctor(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case doctorStartedMsg:
		if m.doc.running {
			return m, nil // already in flight; ignore the duplicate start
		}
		m.doc.running = true
		m.doc.err = nil
		return m, m.doctorWork()

	case doctorDoneMsg:
		m.doc.running = false
		m.doc.done = true
		m.doc.path = msg.path
		if msg.err != nil {
			m.doc.err = msg.err
			return m, sayStatus(strings.ReplaceAll(msg.err.Error(), "\n", " "), true)
		}
		m.doc.err = nil
		m.doc.results = msg.results
		m.doc.at = msg.at
		ok, total := m.doc.tally()
		return m, sayStatus(fmt.Sprintf("doctor: %d/%d agents replied OK", ok, total), ok < total)

	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "r":
			if m.doc.running {
				return m, sayStatus("already checking…", false)
			}
			return m, m.runDoctor()
		}
	}
	return m, nil
}

// viewDoctor renders the check: a results table (agent, ✔/✖, duration, reply
// or error), a tally pill, and — while a run is in flight — the spinner
// instead of stale rows.
func (m Model) viewDoctor(w, h int) string {
	d := m.doc
	frame := lipgloss.NewStyle().Padding(1, 2).Width(w).Height(h)
	var b strings.Builder

	b.WriteString(h1Style.Render("Doctor"))
	switch {
	case d.running:
		b.WriteString(" " + pill(colorInfo, "running"))
	case d.err != nil:
		b.WriteString(" " + pill(colorBad, "failed"))
	case d.done:
		ok, total := d.tally()
		if ok == total {
			b.WriteString(" " + pill(colorOK, fmt.Sprintf("%d/%d ok", ok, total)))
		} else {
			b.WriteString(" " + pill(colorWarn, fmt.Sprintf("%d/%d ok", ok, total)))
		}
	default:
		b.WriteString(" " + pill(colorMuted, "idle"))
	}
	b.WriteString("\n" + mutedStyle.Render("asks every agent for exactly OK, read-only, in a scratch dir") + "\n")
	if d.path != "" {
		b.WriteString(mutedStyle.Render(truncate(d.path, max(10, w-4))) + "\n")
	}
	b.WriteString("\n")

	switch {
	case d.running:
		b.WriteString("  " + m.spinner.View() + accentStyle.Render(" checking every agent…") + "\n")
		b.WriteString("  " + mutedStyle.Render("each gets up to 3 minutes; the table appears when all answer") + "\n")

	case d.err != nil:
		b.WriteString("  " + badStyle.Render(truncate(strings.ReplaceAll(d.err.Error(), "\n", "  "), max(10, w-4))) + "\n")

	case !d.done:
		b.WriteString("  " + mutedStyle.Render("no check has run yet.") + "\n")

	case len(d.results) == 0:
		b.WriteString("  " + mutedStyle.Render("the config has no agents to check.") + "\n")

	default:
		const nameW, timeW = 16, 8
		// Frame width minus padding, name and time columns, the leading
		// indent and the two-space gaps around the mark: 4+16+8+9 = 37.
		replyW := max(12, w-4-nameW-timeW-9)
		b.WriteString("  " + mutedStyle.Render(
			fmt.Sprintf("%-*s  %-2s  %-*s  %s", nameW, "AGENT", "", timeW, "TIME", "REPLY")) + "\n")
		// Room for the rows given the fixed chrome around them (frame padding,
		// header, path, column header, "checked" block, footer); the table has
		// no cursor to follow, so clipping the tail is safe.
		room := h - 11
		if room < 1 {
			room = 1
		}
		shown := d.results
		hidden := 0
		if len(shown) > room {
			hidden = len(shown) - room
			shown = shown[:room]
		}
		for _, r := range shown {
			mark, detail := badStyle.Render("✖"), badStyle.Render(truncate(r.errText, replyW))
			if r.ok {
				mark = okStyle.Render("✔")
				text := r.reply
				if strings.TrimSpace(text) == "" {
					text = "—"
				}
				detail = mutedStyle.Render(truncate(text, replyW))
			}
			b.WriteString("  " +
				titleStyle.Render(fmt.Sprintf("%-*s", nameW, truncate(r.name, nameW))) + "  " +
				mark + "  " +
				mutedStyle.Render(fmt.Sprintf("%-*s", timeW, truncate(r.took.String(), timeW))) + "  " +
				detail + "\n")
		}
		if hidden > 0 {
			b.WriteString("  " + mutedStyle.Render(fmt.Sprintf("… %d more", hidden)) + "\n")
		}
		if !d.at.IsZero() {
			b.WriteString("\n  " + mutedStyle.Render("checked "+started(d.at)) + "\n")
		}
	}

	b.WriteString("\n  " + mutedStyle.Render(doctorKeysHint))
	return frame.Render(b.String())
}

const doctorKeysHint = "⏎ / r run again · esc back"
