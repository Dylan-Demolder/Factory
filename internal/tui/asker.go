package tui

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/pipeline"
)

// refreshInterviewMsg asks the program to redraw: the interview runs on its
// own goroutine, and this is how it tells the UI that something happened.
type refreshInterviewMsg struct{}

// Line is one entry in the interview transcript.
type Line struct {
	Role  string // agent | user | system | error
	Text  string
	Quick []string
}

// Asker is ui.Asker backed by the terminal. Like the web interface's chat, it
// blocks in Ask until the human answers, and doubles as an io.Writer so the
// pipeline's own log lines land in the same transcript.
type Asker struct {
	notify func()

	mu      sync.Mutex
	lines   []Line
	waiting bool
	quick   []string

	answers chan string
	closed  chan struct{}
	once    sync.Once
}

// NewAsker builds an interview transcript. notify is called (from whatever
// goroutine) whenever it changes; the program marshals that into a redraw.
func NewAsker(notify func()) *Asker {
	return &Asker{
		notify: notify,
		// Buffered so Answer never has to wait for Ask to reach its select.
		// Ask unlocks, fires a redraw and only then blocks, so an answer that
		// arrived in that gap used to find no receiver and be dropped by
		// Answer's default branch — the reply vanished and the interview
		// hung. The waiting flag already admits at most one outstanding
		// answer, so a single slot can never overflow.
		answers: make(chan string, 1),
		closed:  make(chan struct{}),
	}
}

func (a *Asker) fire() {
	if a.notify != nil {
		a.notify()
	}
}

func (a *Asker) add(role, text string, quick []string) {
	a.mu.Lock()
	a.lines = append(a.lines, Line{Role: role, Text: strings.TrimRight(text, "\n"), Quick: quick})
	a.mu.Unlock()
	a.fire()
}

// Say records a message from the agent.
func (a *Asker) Say(format string, args ...any) {
	if s := strings.TrimSpace(fmt.Sprintf(format, args...)); s != "" {
		a.add("agent", s, nil)
	}
}

// System records a status line that is not an agent speaking.
func (a *Asker) System(format string, args ...any) {
	if s := strings.TrimSpace(fmt.Sprintf(format, args...)); s != "" {
		a.add("system", s, nil)
	}
}

// Error records a failure.
func (a *Asker) Error(format string, args ...any) {
	a.add("error", strings.TrimSpace(fmt.Sprintf(format, args...)), nil)
}

// Write lets the engine's log output flow into the transcript.
func (a *Asker) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\n")
	if strings.TrimSpace(text) != "" {
		a.add("log", text, nil)
	}
	return len(p), nil
}

// Ask blocks until the human answers. quick are the offered shortcuts.
func (a *Asker) Ask(question string, quick ...string) (string, error) {
	// An answer handed over by a previous Ask that this one never collected
	// (it returned through `closed`) would otherwise be delivered to the
	// wrong question. Nothing can be legitimately pending here: the waiting
	// flag was false on entry, and only Ask raises it.
	select {
	case <-a.answers:
	default:
	}
	a.mu.Lock()
	a.quick = quick
	a.waiting = true
	a.lines = append(a.lines, Line{Role: "agent", Text: question, Quick: quick})
	a.mu.Unlock()
	a.fire()

	select {
	case ans := <-a.answers:
		return ans, nil
	case <-a.closed:
		// Mirror the web Chat: without this, waiting stays true after a
		// close and later answers look accepted while nobody will read them.
		a.mu.Lock()
		a.waiting = false
		a.mu.Unlock()
		return "", context.Canceled
	}
}

// ErrNotWaiting is returned by Answer when no question is outstanding — the
// human replied after the agent had already moved on.
var ErrNotWaiting = fmt.Errorf("nothing is waiting for an answer")

func (a *Asker) Answer(text string) error {
	a.mu.Lock()
	if !a.waiting {
		a.mu.Unlock()
		return ErrNotWaiting
	}
	a.waiting, a.quick = false, nil
	a.lines = append(a.lines, Line{Role: "user", Text: text})
	a.mu.Unlock()
	a.fire()
	select {
	case a.answers <- text:
		return nil
	default:
		return ErrNotWaiting
	}
}

// AskMany collects the round's answers. The terminal costs nothing per
// turn — there is no server round trip between questions — so this steps
// through them one at a time while still returning the whole batch, which
// is the contract the interview loop and the browser rely on.
func (a *Asker) AskMany(questions []string, quick ...string) ([]string, error) {
	answers := make([]string, 0, len(questions))
	for _, q := range questions {
		ans, err := a.Ask(q, quick...)
		if err != nil && err != io.EOF {
			return answers, err
		}
		answers = append(answers, ans)
		if strings.TrimSpace(ans) == "/done" || err == io.EOF {
			// Fill the rest so indexes still line up, then stop.
			for len(answers) < len(questions) {
				answers = append(answers, "")
			}
			return answers, err
		}
	}
	return answers, nil
}

// Close releases a blocked Ask, e.g. when the user leaves the interview.
func (a *Asker) Close() {
	a.once.Do(func() { close(a.closed) })
}

// Snapshot returns the transcript for rendering.
func (a *Asker) Snapshot() (lines []Line, waiting bool, quick []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Line, len(a.lines))
	copy(out, a.lines)
	return out, a.waiting, a.quick
}

// interview drives one spec session on its own goroutine, mirroring how the
// web interface starts it: engine.Spec blocks asking questions until the
// human approves or the session is cancelled.
type interview struct {
	Asker  *Asker
	cancel context.CancelFunc
	active bool
	err    string
}

// startInterview opens the spec interview for pr. The build stays detached:
// approving the spec hands off to app.StartRun, which forks a process that
// outlives this UI.
func (m *Model) startInterview(pr *app.Project) tea.Cmd {
	if m.proj.session != nil && m.proj.session.active {
		return sayStatus("an interview is already in progress", true)
	}
	asker := NewAsker(func() { m.notifyRedraw() })
	ctx, cancel := context.WithCancel(context.Background())
	sess := &interview{Asker: asker, cancel: cancel, active: true}
	m.proj.session = sess

	e, err := pr.Engine(asker)
	if err != nil {
		cancel()
		m.proj.session = nil
		return sayStatus(err.Error(), true)
	}
	e.UI = asker
	sess.Asker.System("Interview started. Answers are saved as you go.")

	go func() {
		defer cancel()
		err := e.Spec(ctx)
		switch {
		case err != nil && ctx.Err() != nil:
			sess.Asker.System("Interview paused. Your answers are saved; resume any time.")
		case err != nil:
			sess.Asker.Error("%s", "The interview stopped: "+err.Error())
			sess.err = err.Error()
		default:
			sess.Asker.System("✔ Spec approved and committed as SPEC.md.")
			if pid, err := pr.StartRun(exePath()); err != nil {
				sess.Asker.Error("%s", "Could not start the build: "+err.Error())
			} else {
				sess.Asker.System("▶ Build started in the background (pid %d).", pid)
			}
		}
		sess.Asker.Close()
		sess.active = false
		m.notifyRedraw()
	}()
	return nil
}

// notifyRedraw asks the program to repaint. The interview runs on its own
// goroutine, so this is its only way to reach the UI — and send is not wired
// up until Run starts, hence the guard.
func (m *Model) notifyRedraw() {
	if m.send != nil {
		m.send(refreshInterviewMsg{})
	}
}

// stopInterview cancels a session without losing saved answers.
func (m *Model) stopInterview() tea.Cmd {
	if m.proj.session == nil || !m.proj.session.active {
		return sayStatus("no interview running", true)
	}
	m.proj.session.cancel()
	m.proj.session.active = false
	return sayStatus("interview paused — answers are kept", false)
}

// newInterviewMsg is posted when a project needs its interview opened.
type openProjectMsg struct {
	id  string
	err error
}

// openProject loads a project and shows it.
func (m Model) openProject(p app.Summary) tea.Cmd {
	target := p
	return func() tea.Msg {
		pr, err := app.Open(target.Dir, m.CfgPath)
		if err != nil {
			return openProjectMsg{id: target.ID, err: err}
		}
		return projectLoadedMsg{pr: pr, sum: target}
	}
}

type projectLoadedMsg struct {
	pr             *app.Project
	sum            app.Summary
	err            error
	startInterview bool
}

// exePath is the factory binary, used to launch detached builds.
func exePath() string {
	// os.Executable can fail in odd sandboxes; the CLI then falls back to PATH
	// resolution inside app.StartRun callers. Resolve once at startup.
	return currentExe
}

var currentExe = ""

func init() {
	// Resolved in Run's caller (cmd/factory) via SetExe.
}

// SetExe records the running binary so builds can be detached from it.
func SetExe(path string) { currentExe = path }

// started is used for "when" labels.
func started(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

var _ = pipeline.Engine{}
