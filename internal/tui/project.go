package tui

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/state"
)

// The project workspace tabs, in row order. They wrap in both directions.
const (
	tabOverview = iota
	tabInterview
	tabLog
	tabFiles
	tabTasks
)

var projectTabLabels = []string{"Overview", "Interview", "Log", "Files", "Tasks"}

// Budgets so a giant run.log or artifact cannot blow up memory: we tail and
// cap instead of slurping.
const (
	maxLogRead  = 256 * 1024 // bytes pulled from run.log per refresh
	maxLogKeep  = 128 * 1024 // raw run.log kept in memory
	maxFileRead = 256 * 1024 // files opened in the Files tab
	maxFileRows = 400        // artifact rows listed
)

// projectState is the screen's state. Everything that must survive between
// draws lives here; the view never mutates it (it works on copies).
type projectState struct {
	pr     *app.Project
	sum    app.Summary    // listing summary, refreshed on every tick
	st     *state.Project // freshly loaded copy for display; the engine owns pr.P
	report bool           // REPORT.md exists on disk

	tab     int
	session *interview // nil when no interview is running

	// Tasks tab: the selected row, and a pending second press of u —
	// re-queueing resets an attempt counter, so like deleting it takes two.
	taskSel     int
	taskConfirm string

	// The answer box. It is focused only on the Interview tab while an
	// interview can take answers — exactly when typing() must swallow the
	// global shortcuts.
	input      textinput.Model
	chat       viewport.Model
	chatFollow bool // transcript stays pinned to the newest line

	logView viewport.Model
	logRaw  string // raw bytes of run.log we kept
	logBuf  string // logRaw word-wrapped to the pane width
	logOff  int64  // read position in run.log
	logSeen bool   // run.log exists
	follow  bool   // tail the log, but only while it is already at the bottom

	rows     []projFileRow
	rowCur   int
	openRel  string // file being viewed; "" = the list
	fileView viewport.Model
}

// typing reports that this screen owns the keyboard: while the answer box has
// focus, and while a file is open (the file view needs esc and q for itself).
// Global shortcuts — q, /, 1-4 — must not fire in either case.
func (s projectState) typing() bool {
	return s.input.Focused() || s.openRel != ""
}

// loadProject swaps in a newly opened project and returns the commands the
// screen needs: the first state/log refresh and the tick that re-arms it.
func (m *Model) loadProject(pr *app.Project, sum app.Summary) tea.Cmd {
	if pr == nil {
		return sayStatus("could not load that project", true)
	}
	// Leaving one project for another must not keep the old interview
	// answering into a screen nobody watches. Re-opening the same project
	// leaves a live session alone.
	if old := m.proj.session; old != nil && old.active &&
		m.proj.pr != nil && m.proj.pr.Store.Root != pr.Store.Root {
		old.cancel()
	}
	m.proj = projectState{
		pr:         pr,
		sum:        sum,
		tab:        tabOverview,
		input:      textinput.New(),
		chat:       viewport.New(0, 0),
		logView:    viewport.New(0, 0),
		fileView:   viewport.New(0, 0),
		chatFollow: true,
		follow:     true, // a build log you have to chase is no help
	}
	m.proj.input.Prompt = accentStyle.Render("› ")
	m.proj.input.Placeholder = "type your answer…"
	m.proj.input.CharLimit = 4000
	m.proj.rows, m.proj.rowCur = buildFileRows(pr.Store.Root)
	m.sizeProjectPanes()
	// Nothing else seeds the 2s refresh chain, so it starts here.
	// The root owns the refresh heartbeat (Init arms it and the refreshMsg
	// handler re-arms it), so seeding another one here would double the rate.
	return m.projectRefresh()
}

// projPane mirrors View's layout maths so Update can wrap text and size
// viewports exactly as viewProject will draw them: the view itself must stay
// pure, so the stored viewports have to be right before it renders.
func (m Model) projPane() (w, pane int) {
	sidebarW := clamp(m.width*26/100, 22, 34)
	w = m.width - sidebarW - 1
	pane = m.height - 6 // shell title/status/help + our title/tabs/hint
	if w < 8 {
		w = 8
	}
	if pane < 1 {
		pane = 1
	}
	return w, pane
}

// interviewBudget divides the interview pane: how many rows sit under the
// transcript, and the snapshot bits those rows depend on. Update and View
// share it so scroll maths always matches what is drawn.
func (m Model) interviewBudget(pane int) (chatH int, waiting bool, quick []string) {
	below := 2 // status line + answer box
	if s := m.proj.session; s != nil && s.Asker != nil {
		_, waiting, quick = s.Asker.Snapshot()
		// On a tiny pane the shortcut row goes before anything essential.
		if waiting && len(quick) > 0 && pane >= 4 {
			below++
		}
	}
	return max(1, pane-below), waiting, quick
}

// sizeProjectPanes writes the current geometry into the stored widgets.
func (m *Model) sizeProjectPanes() {
	w, pane := m.projPane()
	chatH, _, _ := m.interviewBudget(pane)
	m.proj.input.Width = max(8, w-4)
	m.proj.chat.Width, m.proj.chat.Height = w, chatH
	m.proj.logView.Width, m.proj.logView.Height = w, pane
	m.proj.fileView.Width, m.proj.fileView.Height = w, pane
}

// setProjTab moves to a tab (wrapping) and blurs the answer box: only the
// Interview tab may hold it, and only while an interview can take answers.
func (m *Model) setProjTab(i int) tea.Cmd {
	m.proj.tab = ((i % len(projectTabLabels)) + len(projectTabLabels)) % len(projectTabLabels)
	m.proj.input.Blur()
	switch m.proj.tab {
	case tabLog:
		if m.proj.follow {
			m.proj.logView.GotoBottom()
		}
	case tabFiles:
		if m.proj.openRel == "" && m.proj.pr != nil {
			// Re-list: REPORT.md and roundtables appear as the build runs.
			m.proj.rows, m.proj.rowCur = buildFileRows(m.proj.pr.Store.Root)
		}
	case tabInterview:
		if s := m.proj.session; s != nil && s.active {
			return m.proj.input.Focus()
		}
	case tabTasks:
		m.proj.taskConfirm = ""
		if st := m.proj.st; st != nil {
			m.proj.taskSel = firstBlockedTask(st.Tasks)
		}
	}
	return nil
}

// ---- refresh ----

// projectRefreshedMsg is what projectRefresh() returns: one sweep of disk
// state (fresh summary, acceptance round, REPORT.md) plus whatever run.log
// has gained since the stored offset.
type projectRefreshedMsg struct {
	sum    app.Summary
	sumOK  bool
	st     *state.Project
	report bool

	logErr    error // non-nil: leave the log untouched this round
	logExists bool
	logReset  bool // the file shrank (rotate/truncate): start over
	logChunk  string
	logOff    int64
}

// projectRefresh re-reads project state and tails run.log from the stored
// offset, so phase, progress and log lines move without a keypress. tui.go
// calls it every ~2s while this screen is visible.
func (m Model) projectRefresh() tea.Cmd {
	pr, sum, off := m.proj.pr, m.proj.sum, m.proj.logOff
	if pr == nil {
		return nil
	}
	return func() tea.Msg {
		msg := projectRefreshedMsg{logOff: off}
		if sum.Dir != "" {
			if s, err := app.Summarize(sum.ID, sum.Dir); err == nil {
				msg.sum, msg.sumOK = s, true
			}
		}
		// A separate copy: the interview/build goroutine owns pr.P, so
		// reading it here would race. Store.Load makes its own object.
		if st, err := pr.Store.Load(); err == nil {
			msg.st = st
		}
		if _, err := os.Stat(filepath.Join(pr.Store.Root, "REPORT.md")); err == nil {
			msg.report = true
		}

		path := filepath.Join(pr.Store.Root, ".factory", "run.log")
		f, err := os.Open(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// A project that has never built simply has no log.
			msg.logExists = false
		case err != nil:
			msg.logErr = err
		default:
			defer f.Close()
			fi, serr := f.Stat()
			switch {
			case serr != nil:
				msg.logErr = serr
			default:
				start := off
				if fi.Size() < off { // rotated or truncated under us
					start = 0
					msg.logReset = true
				}
				if _, err := f.Seek(start, io.SeekStart); err != nil {
					msg.logErr = err
					break
				}
				data, _ := io.ReadAll(io.LimitReader(f, maxLogRead))
				msg.logExists = true
				msg.logChunk = string(data)
				msg.logOff = start + int64(len(data))
			}
		}
		return msg
	}
}

// applyRefresh folds a projectRefreshedMsg into the screen: fresh counters,
// and the new log bytes appended to the viewport.
func (m Model) applyRefresh(msg projectRefreshedMsg) (tea.Model, tea.Cmd) {
	m.sizeProjectPanes()
	if msg.sumOK {
		m.proj.sum = msg.sum
	}
	if msg.st != nil {
		m.proj.st = msg.st
	}
	m.proj.report = msg.report
	w, _ := m.projPane()

	// Keep the stored transcript in sync so scroll keys measure real content.
	m.proj.chat.SetContent(m.chatContent(w))
	if m.proj.chatFollow {
		m.proj.chat.GotoBottom()
	}

	if msg.logErr != nil {
		return m, nil
	}
	if !msg.logExists {
		if m.proj.logSeen || m.proj.logRaw != "" {
			m.proj.logRaw, m.proj.logBuf = "", ""
			m.proj.logView.SetContent("")
			m.proj.logView.GotoTop()
		}
		m.proj.logSeen, m.proj.logOff = false, 0
		return m, nil
	}
	// Was the reader already on the newest line? Only then may follow mode
	// pull it down — never yank a scroll position the user chose.
	atBottom := m.proj.logView.AtBottom()
	if msg.logReset {
		m.proj.logRaw = ""
	}
	if msg.logChunk != "" {
		m.proj.logRaw += msg.logChunk
		m.proj.trimLog()
	}
	m.proj.logSeen, m.proj.logOff = true, msg.logOff
	if msg.logReset || msg.logChunk != "" {
		m.proj.logBuf = wrapToWidth(m.proj.logRaw, w)
		m.proj.logView.SetContent(m.proj.logBuf)
		if m.proj.follow && atBottom {
			m.proj.logView.GotoBottom()
		}
	}
	return m, nil
}

// trimLog drops the oldest kept bytes once the raw log passes its cap,
// breaking on a newline so no line is ever half-visible.
func (s *projectState) trimLog() {
	if len(s.logRaw) <= maxLogKeep {
		return
	}
	tail := len(s.logRaw) - maxLogKeep
	if i := strings.IndexByte(s.logRaw[tail:], '\n'); i >= 0 {
		s.logRaw = s.logRaw[tail+i+1:]
		return
	}
	s.logRaw = s.logRaw[tail:]
}

// ---- update ----

func (m Model) updateProject(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case projectRefreshedMsg:
		return m.applyRefresh(msg)
	case refreshInterviewMsg:
		return m, nil // the transcript changed; the next draw picks it up
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case taskRetryMsg:
		if msg.err != nil {
			return m, sayStatus(msg.err.Error(), true)
		}
		text := "re-queued " + msg.id
		if len(msg.released) > 0 {
			text += " · released " + strings.Join(msg.released, ", ")
		}
		return m, tea.Batch(m.projectRefresh(),
			sayStatus(text+" — start the build to run it", false))
	case tea.KeyMsg:
		return m.projectKey(msg)
	}
	// Anything else — the answer box blinking, mostly — belongs to the input.
	if m.proj.input.Focused() {
		var cmd tea.Cmd
		m.proj.input, cmd = m.proj.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) projectKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.sizeProjectPanes()
	if m.proj.pr == nil {
		return m, nil
	}
	key := msg.String()

	// The answer box owns every printable key while it has focus; only the
	// keys that steer the interview itself are intercepted first.
	if m.proj.input.Focused() {
		switch key {
		case "enter":
			return m.interviewSend()
		case "esc":
			m.proj.input.Blur() // stop typing; a second esc leaves the screen
			return m, nil
		case "ctrl+c":
			m.proj.input.Blur()
			return m, m.stopInterview()
		case "tab":
			m.proj.input.Blur()
			return m, m.setProjTab(m.proj.tab + 1)
		case "shift+tab":
			m.proj.input.Blur()
			return m, m.setProjTab(m.proj.tab - 1)
		default:
			var cmd tea.Cmd
			m.proj.input, cmd = m.proj.input.Update(msg)
			return m, cmd
		}
	}

	// Tab steering works from every tab, so h/l never fight the widgets.
	switch key {
	case "tab", "right", "l":
		return m, m.setProjTab(m.proj.tab + 1)
	case "shift+tab", "left", "h":
		return m, m.setProjTab(m.proj.tab - 1)
	}

	switch m.proj.tab {
	case tabInterview:
		return m.interviewKey(msg, key)
	case tabLog:
		return m.logKey(msg, key)
	case tabFiles:
		return m.filesKey(msg, key)
	case tabTasks:
		return m.tasksKey(msg, key)
	default:
		return m.overviewKey(key)
	}
}

// overviewKey runs the phase's headline action on ⏎.
func (m Model) overviewKey(key string) (tea.Model, tea.Cmd) {
	pr := m.proj.pr
	switch key {
	case "enter":
		switch act, _ := m.overviewAction(); act {
		case projActInterview:
			cmd := m.startInterview(pr)
			if cmd == nil {
				cmd = sayStatus("interview started", false)
			}
			return m, tea.Batch(cmd, loadProjects(m.Workspace), m.setProjTab(tabInterview))
		case projActOpenInterview:
			return m, m.setProjTab(tabInterview)
		case projActStart:
			pid, err := pr.StartRun(exePath())
			if err != nil {
				return m, tea.Batch(loadProjects(m.Workspace), sayStatus(err.Error(), true))
			}
			return m, tea.Batch(loadProjects(m.Workspace),
				sayStatus(fmt.Sprintf("build started (pid %d)", pid), false))
		case projActStop:
			if err := pr.Stop(); err != nil {
				return m, tea.Batch(loadProjects(m.Workspace), sayStatus(err.Error(), true))
			}
			return m, tea.Batch(loadProjects(m.Workspace), sayStatus("stopping the build", false))
		}
	case "r":
		// Re-open a settled project for a fresh spec; code and git stay.
		if !m.proj.sum.Running && m.projPhase() != state.PhaseSpec {
			if err := pr.ResetForSpec(); err != nil {
				return m, sayStatus(err.Error(), true)
			}
			return m, tea.Batch(loadProjects(m.Workspace),
				sayStatus("reset to spec — ⏎ to start the interview", false))
		}
	}
	return m, nil
}

// interviewKey drives the Interview tab while the answer box is blurred.
func (m Model) interviewKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	s := m.proj.session
	switch key {
	case "enter":
		if s == nil {
			if m.projPhase() != state.PhaseSpec {
				return m, nil // the empty state has nothing to offer here
			}
			cmd := m.startInterview(m.proj.pr)
			if cmd == nil {
				cmd = sayStatus("interview started", false)
			}
			return m, tea.Batch(cmd, loadProjects(m.Workspace), m.setProjTab(tabInterview))
		}
		if !s.active {
			return m.resumeInterview()
		}
		return m, m.proj.input.Focus() // interview is live: start typing
	case "p", "ctrl+c":
		if s == nil || !s.active {
			return m, sayStatus("no interview running", true)
		}
		return m, m.stopInterview()
	}
	if s == nil || s.Asker == nil {
		return m, nil
	}
	w, _ := m.projPane()
	m.proj.chat.SetContent(m.chatContent(w)) // measure against real content
	switch key {
	case "g":
		m.proj.chat.GotoTop()
		m.proj.chatFollow = false
		return m, nil
	case "G":
		m.proj.chat.GotoBottom()
		m.proj.chatFollow = true
		return m, nil
	}
	var cmd tea.Cmd
	m.proj.chat, cmd = m.proj.chat.Update(msg)
	m.proj.chatFollow = m.proj.chat.AtBottom()
	return m, cmd
}

// interviewSend hands the typed line to the waiting question.
func (m Model) interviewSend() (tea.Model, tea.Cmd) {
	s := m.proj.session
	if s == nil || s.Asker == nil {
		m.proj.input.Blur()
		return m, sayStatus("no interview is waiting", true)
	}
	if !s.active {
		return m.resumeInterview()
	}
	if err := s.Asker.Answer(m.proj.input.Value()); err != nil {
		// ErrNotWaiting just means the agent has moved on — say so and keep
		// the text, it may still be worth sending later.
		return m, sayStatus(err.Error(), true)
	}
	m.proj.input.SetValue("")
	return m, nil
}

// resumeInterview restarts a paused session; answers live in state.json, so
// the new engine picks up where the old one stopped.
func (m Model) resumeInterview() (tea.Model, tea.Cmd) {
	if m.projPhase() != state.PhaseSpec {
		return m, sayStatus("the interview is finished — see Overview", false)
	}
	cmd := m.startInterview(m.proj.pr)
	if cmd == nil {
		cmd = sayStatus("interview resumed", false)
	}
	return m, tea.Batch(cmd, m.setProjTab(tabInterview))
}

// logKey scrolls the tail: g/G jump, / toggles follow, everything else goes
// to the viewport's own key map (↑↓, pgup/pgdn, u/d, f/b, space).
func (m Model) logKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	switch key {
	case "g":
		m.proj.logView.GotoTop()
		return m, nil
	case "G":
		m.proj.follow = true // jumping to the end re-engages the tail
		m.proj.logView.GotoBottom()
		return m, nil
	case "/":
		m.proj.follow = !m.proj.follow
		if m.proj.follow {
			m.proj.logView.GotoBottom()
			return m, sayStatus("following the log", false)
		}
		return m, sayStatus("log follow off — G to jump to the end", false)
	}
	var cmd tea.Cmd
	m.proj.logView, cmd = m.proj.logView.Update(msg)
	return m, cmd
}

// filesKey moves through the artifact list, or scrolls an open file.
func (m Model) filesKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	// While a file is open typing() is true, so esc and q arrive here
	// instead of leaving the screen or quitting.
	if m.proj.openRel != "" {
		switch key {
		case "esc", "q":
			m.proj.openRel = ""
			return m, nil
		case "g":
			m.proj.fileView.GotoTop()
			return m, nil
		case "G":
			m.proj.fileView.GotoBottom()
			return m, nil
		}
		var cmd tea.Cmd
		m.proj.fileView, cmd = m.proj.fileView.Update(msg)
		return m, cmd
	}
	switch key {
	case "up", "k":
		m.proj.moveRow(-1)
	case "down", "j":
		m.proj.moveRow(1)
	case "g":
		m.proj.rowCur = m.proj.edgeRow(-1)
	case "G":
		m.proj.rowCur = m.proj.edgeRow(1)
	case "enter":
		return m, m.openProjFile()
	}
	return m, nil
}

// projAction is what ⏎ does on the Overview tab.
type projAction int

const (
	projActNone projAction = iota
	projActInterview
	projActOpenInterview
	projActStart
	projActStop
)

// overviewAction picks the one action that matters for the current phase:
// spec wants the interview, an approved project wants a build, a running
// build wants to be stoppable.
func (m Model) overviewAction() (projAction, string) {
	if m.proj.sum.Running {
		return projActStop, "⏎ stop build"
	}
	switch m.projPhase() {
	case state.PhaseSpec:
		if s := m.proj.session; s != nil && s.active {
			return projActOpenInterview, "⏎ open interview"
		}
		return projActInterview, "⏎ start interview"
	case state.PhaseDone:
		return projActStart, "⏎ run again"
	default:
		return projActStart, "⏎ start build"
	}
}

// projPhase is the phase to act on: the freshly loaded copy when we have one,
// else the summary the project was opened with.
func (m Model) projPhase() string {
	if m.proj.st != nil && m.proj.st.Phase != "" {
		return m.proj.st.Phase
	}
	return m.proj.sum.Phase
}

// ---- files ----

// projFileRow is one line of the Files tab: a section heading, a placeholder,
// or a path relative to the project root.
type projFileRow struct {
	heading bool
	label   string // path relative to the root, or a section title
	missing bool   // listed but not on disk (root docs)
	noPick  bool   // heading/placeholder: skipped by the cursor
}

// buildFileRows lists the project's artifacts: the root documents first,
// then everything under .factory grouped by directory.
func buildFileRows(root string) ([]projFileRow, int) {
	rows := []projFileRow{{heading: true, label: "Project", noPick: true}}
	for _, name := range []string{"SPEC.md", "REPORT.md", "README.md"} {
		_, err := os.Stat(filepath.Join(root, name))
		rows = append(rows, projFileRow{label: name, missing: err != nil})
	}

	var dirs []string // walk order: a parent is always listed before its children
	byDir := map[string][]string{}
	fd := filepath.Join(root, ".factory")
	if fi, err := os.Stat(fd); err == nil && fi.IsDir() {
		// WalkDir does not follow symlinks, so this stays inside the tree.
		filepath.WalkDir(fd, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entries are simply skipped
			}
			if d.IsDir() {
				dirs = append(dirs, path)
				return nil
			}
			dir := filepath.Dir(path)
			byDir[dir] = append(byDir[dir], d.Name())
			return nil
		})
	} else {
		rows = append(rows,
			projFileRow{heading: true, label: ".factory", noPick: true},
			projFileRow{label: "(none yet — start a build)", noPick: true})
	}

	for _, dir := range dirs {
		relDir, err := filepath.Rel(root, dir)
		if err != nil {
			continue
		}
		relDir = filepath.ToSlash(relDir)
		rows = append(rows, projFileRow{heading: true, label: relDir, noPick: true})
		names := byDir[dir]
		sort.Strings(names)
		if len(names) == 0 {
			rows = append(rows, projFileRow{label: "(empty)", missing: true, noPick: true})
		}
		for _, n := range names {
			if len(rows) >= maxFileRows {
				break
			}
			rows = append(rows, projFileRow{label: relDir + "/" + n})
		}
		if len(rows) >= maxFileRows {
			break
		}
	}

	cur := 0
	for i, r := range rows {
		if !r.heading && !r.noPick {
			cur = i
			break
		}
	}
	return rows, cur
}

// moveRow steps the cursor to the next selectable row in dir.
func (s *projectState) moveRow(dir int) {
	for i := s.rowCur + dir; i >= 0 && i < len(s.rows); i += dir {
		if !s.rows[i].heading && !s.rows[i].noPick {
			s.rowCur = i
			return
		}
	}
}

// edgeRow is the first (-1) or last (+1) selectable row.
func (s *projectState) edgeRow(dir int) int {
	for i := 0; i < len(s.rows); i++ {
		idx := i
		if dir > 0 {
			idx = len(s.rows) - 1 - i
		}
		if !s.rows[idx].heading && !s.rows[idx].noPick {
			return idx
		}
	}
	return s.rowCur
}

// openProjFile reads the selected artifact into the file viewport. The path
// is resolved against the project root and refused unless the cleaned path
// stays inside it.
func (m *Model) openProjFile() tea.Cmd {
	if m.proj.rowCur < 0 || m.proj.rowCur >= len(m.proj.rows) {
		return nil
	}
	row := m.proj.rows[m.proj.rowCur]
	if row.heading || row.noPick {
		return nil
	}
	root, err := filepath.Abs(m.proj.pr.Store.Root)
	if err != nil {
		return sayStatus(err.Error(), true)
	}
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(row.label)))
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return sayStatus("refused: "+row.label+" is outside the project", true)
	}
	// A limited read: run.log can be huge and only 256 KB is ever shown.
	f, err := os.Open(path)
	if err != nil {
		return sayStatus(err.Error(), true)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFileRead+1))
	if err != nil {
		return sayStatus(err.Error(), true)
	}
	truncated := len(data) > maxFileRead
	if truncated {
		data = data[:maxFileRead]
	}
	text := string(data)
	if truncated {
		text += "\n\n… truncated at 256 KB"
	}
	w, _ := m.projPane()
	m.proj.fileView.SetContent(wrapToWidth(text, w))
	m.proj.fileView.GotoTop()
	m.proj.openRel = row.label
	return nil
}

// ---- view ----

// viewProject draws the whole screen: title, tabs, the tab body and a
// context-sensitive key hint, clamped to the exact pane it is given.
func (m Model) viewProject(w, h int) string {
	if m.proj.pr == nil {
		return clipBlock(mutedStyle.Render("  Opening project…"), h)
	}
	if w < 8 {
		w = 8
	}
	pane := max(1, h-3) // title + tabs + hint leave h-3 for the body
	rows := []string{
		m.projectTitleRow(w),
		m.projectTabRow(w),
		m.projectBody(w, pane),
		helpStyle.Render(truncate(m.projectKeys(), w)),
	}
	return clipBlock(strings.Join(rows, "\n"), h)
}

func (m Model) projectBody(w, pane int) string {
	var body string
	switch m.proj.tab {
	case tabInterview:
		body = m.viewInterview(w, pane)
	case tabLog:
		body = m.viewLog(w, pane)
	case tabFiles:
		body = m.viewFiles(w, pane)
	case tabTasks:
		body = m.viewTasks(w, pane)
	default:
		body = m.viewOverview(w, pane)
	}
	return clipBlock(body, pane)
}

func (m Model) projectTitleRow(w int) string {
	name := m.proj.sum.Name
	if name == "" && m.proj.st != nil {
		name = m.proj.st.Name
	}
	if name == "" {
		name = filepath.Base(m.proj.pr.Store.Root)
	}
	suffix := ""
	if m.proj.sum.Running {
		suffix = mutedStyle.Render(fmt.Sprintf("  %s pid %d", runningDot, m.proj.sum.Pid))
	}
	chip := phasePill(m.projPhase(), m.proj.sum.Running)
	row := titleStyle.Render(truncate(name, max(6, w-lipgloss.Width(chip)-4))) + " " + chip + suffix
	// Pills have a fixed width; on a pane too narrow to hold one, the name
	// alone still fits and nothing wraps.
	if lipgloss.Width(row) > w {
		return titleStyle.Render(truncate(name, w))
	}
	return row
}

func (m Model) projectTabRow(w int) string {
	parts := make([]string, 0, len(projectTabLabels))
	for i, label := range projectTabLabels {
		if i == m.proj.tab {
			parts = append(parts, activeTabStyle.Render(label))
		} else {
			parts = append(parts, tabStyle.Render(label))
		}
	}
	row := strings.Join(parts, "")
	if lipgloss.Width(row) > w {
		// Very narrow pane: plain truncated labels beat a wrapped row.
		return mutedStyle.Render(truncate(strings.Join(projectTabLabels, " "), w))
	}
	return row
}

// projectKeys is the context hint line for the visible tab.
func (m Model) projectKeys() string {
	switch m.proj.tab {
	case tabInterview:
		if m.proj.input.Focused() {
			return "⏎ send · esc stop typing · ctrl+c pause · tab next"
		}
		s := m.proj.session
		if s == nil {
			if m.projPhase() == state.PhaseSpec {
				return "⏎ start interview · h/l tabs · esc back"
			}
			return "h/l tabs · esc back"
		}
		if s.active {
			return "⏎ type your answer · p pause · h/l tabs"
		}
		return "⏎ resume · p · h/l tabs · esc back"
	case tabLog:
		follow := "off"
		if m.proj.follow {
			follow = "on"
		}
		return "↑↓ scroll · g top · G end · / follow " + follow + " · h/l tabs"
	case tabFiles:
		if m.proj.openRel != "" {
			return "↑↓ scroll · g/G ends · esc/q close · h/l tabs"
		}
		return "↑↓ move · ⏎ open · h/l tabs · esc back"
	case tabTasks:
		return "↑↓ select · u re-queue blocked (twice) · h/l tabs"
	default:
		_, hint := m.overviewAction()
		if !m.proj.sum.Running && m.projPhase() != state.PhaseSpec {
			hint += " · r new spec"
		}
		return hint + " · h/l tabs · esc back"
	}
}

// viewOverview is the at-a-glance panel: phase, progress, outcome, and the
// one action that matters now.
func (m Model) viewOverview(w, pane int) string {
	sum := m.proj.sum
	phase := m.projPhase()

	done, blocked, total := sum.Done, sum.Blocked, sum.Total
	uses, satisfied, round := sum.UseCases, sum.Satisfied, 0
	outcome := sum.Outcome
	if st := m.proj.st; st != nil {
		done, blocked, total = st.Counts()
		uses = len(st.Spec.UseCases)
		satisfied = 0
		for _, u := range st.Spec.UseCases {
			if u.Verdict == "satisfied" {
				satisfied++
			}
		}
		round = st.AcceptanceRound
		outcome = st.Outcome
	}
	vw := fitW(w)

	var b strings.Builder
	b.WriteString("  " + phasePill(phase, sum.Running))
	if sum.Running {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("  %s pid %d", runningDot, sum.Pid)))
	}
	b.WriteString("\n  " + mutedStyle.Render("updated "+started(sum.Updated)) + "\n")

	b.WriteString(sectionStyle.Render("Progress") + "\n")
	if total > 0 {
		// One truncated value rather than coloured parts: the row can then
		// never exceed the pane, even at 60 columns. Blocked turns it warn.
		taskRaw := fmt.Sprintf("%d/%d done", done, total)
		if blocked > 0 {
			taskRaw += fmt.Sprintf(" · %d blocked", blocked)
		}
		shown := truncate(taskRaw, max(4, w-15))
		if blocked > 0 {
			shown = warnStyle.Render(shown)
		}
		b.WriteString(labelCol("tasks") + shown + "\n")
		if bar := taskBar(done, total, min(24, w-6)); bar != "" {
			b.WriteString("  " + bar + "\n")
		}
	} else {
		b.WriteString(labelCol("tasks") + mutedStyle.Render(truncate("no tasks yet", vw)) + "\n")
	}
	switch {
	case uses == 0:
		b.WriteString(labelCol("use cases") + mutedStyle.Render(truncate("not drafted yet", vw)) + "\n")
	case satisfied == uses:
		b.WriteString(labelCol("use cases") +
			okStyle.Render(truncate(fmt.Sprintf("%d/%d satisfied", satisfied, uses), vw)) + "\n")
	default:
		b.WriteString(labelCol("use cases") +
			warnStyle.Render(truncate(fmt.Sprintf("%d/%d satisfied", satisfied, uses), vw)) + "\n")
	}
	roundVal := "—"
	if round > 0 {
		roundVal = fmt.Sprintf("round %d", round)
	}
	b.WriteString(labelCol("acceptance") + truncate(roundVal, vw) + "\n")

	b.WriteString(sectionStyle.Render("Outcome") + "\n")
	b.WriteString(labelCol("outcome") + outcomeText(outcome, phase, w) + "\n")
	reportVal := mutedStyle.Render(truncate("REPORT.md not written yet", vw))
	if m.proj.report {
		reportVal = okStyle.Render(truncate("REPORT.md written", vw))
	}
	b.WriteString(labelCol("report") + reportVal + "\n")

	_, hint := m.overviewAction()
	b.WriteString("\n  " + accentStyle.Render(hint))
	return clipBlock(b.String(), pane)
}

// viewInterview is the transcript with the answer box under it.
func (m Model) viewInterview(w, pane int) string {
	s := m.proj.session
	if s == nil || s.Asker == nil {
		return m.interviewEmptyState(w, pane)
	}
	chatH, waiting, quick := m.interviewBudget(pane)

	chat := m.proj.chat
	chat.Width, chat.Height = w, chatH
	content := m.chatContent(w)
	if strings.TrimSpace(content) == "" {
		content = mutedStyle.Render("  " + truncate("The transcript will appear here.", max(4, w-2)))
	}
	chat.SetContent(content)
	if m.proj.chatFollow {
		chat.GotoBottom()
	}

	rows := []string{chat.View()}
	if waiting && len(quick) > 0 && pane >= 4 {
		rows = append(rows, mutedStyle.Render(
			truncate("  shortcuts: "+strings.Join(quick, " · "), w)))
	}
	switch {
	case waiting:
		rows = append(rows, "  "+m.spinner.View()+" "+
			accentStyle.Render(truncate("waiting for your answer", max(4, w-6))))
	case s.active:
		rows = append(rows, "  "+m.spinner.View()+" "+mutedStyle.Render("working…"))
	case m.projPhase() == state.PhaseSpec:
		rows = append(rows, "  "+mutedStyle.Render(truncate("interview paused — ⏎ resume", max(4, w-2))))
	default:
		rows = append(rows, "  "+mutedStyle.Render(truncate("interview finished", max(4, w-2))))
	}
	rows = append(rows, "  "+m.proj.input.View())
	return clipBlock(strings.Join(rows, "\n"), pane)
}

// interviewEmptyState explains what the Interview tab is for before there is
// an interview to show.
func (m Model) interviewEmptyState(w, pane int) string {
	if m.projPhase() != state.PhaseSpec {
		var b strings.Builder
		for _, line := range wrapLines(
			"No interview for this project — the spec is settled.", max(8, w-3)) {
			b.WriteString(mutedStyle.Render("  "+line) + "\n")
		}
		b.WriteString("\n")
		for _, line := range wrapLines("Press r on Overview to re-spec it.", max(8, w-3)) {
			b.WriteString(mutedStyle.Render("  "+line) + "\n")
		}
		return clipBlock(b.String(), pane)
	}
	// The card's border and padding eat four columns on top of our indent.
	var b strings.Builder
	b.WriteString(mutedStyle.Render("No interview yet.") + "\n\n")
	b.WriteString(accentStyle.Render("⏎  start interview") + "\n\n")
	for _, line := range wrapLines(
		"factory asks the questions that turn your idea into a spec; "+
			"answers are saved as you go, so you can pause any time.",
		max(8, w-7)) {
		b.WriteString(mutedStyle.Render("  "+line) + "\n")
	}
	return clipBlock(cardStyle.Render(b.String()), pane)
}

// viewLog tails .factory/run.log in a viewport.
func (m Model) viewLog(w, pane int) string {
	if !m.proj.logSeen {
		var b strings.Builder
		for _, text := range []string{"No build log yet.",
			"Start a build and its output streams here."} {
			for _, line := range wrapLines(text, max(8, w-3)) {
				b.WriteString(mutedStyle.Render("  "+line) + "\n")
			}
		}
		return clipBlock(b.String(), pane)
	}
	if m.proj.logRaw == "" {
		return clipBlock(mutedStyle.Render(
			"  "+truncate("run.log is empty — waiting for output…", max(4, w-2))), pane)
	}
	lv := m.proj.logView
	lv.Width, lv.Height = w, pane
	return clipBlock(lv.View(), pane)
}

// viewFiles lists artifacts, or shows the selected one.
func (m Model) viewFiles(w, pane int) string {
	if m.proj.openRel != "" {
		fv := m.proj.fileView
		fv.Width, fv.Height = w, pane
		return clipBlock(fv.View(), pane)
	}
	if len(m.proj.rows) == 0 {
		return clipBlock(mutedStyle.Render("  No artifacts yet."), pane)
	}
	// A window of rows that always keeps the cursor on screen: fill down
	// first, then pull more context in from above.
	end, used := m.proj.rowCur, 1
	for end+1 < len(m.proj.rows) && used < pane {
		end++
		used++
	}
	start := m.proj.rowCur
	for start-1 >= 0 && used < pane {
		start--
		used++
	}
	out := make([]string, 0, used)
	for i := start; i <= end; i++ {
		r := m.proj.rows[i]
		switch {
		case r.heading:
			out = append(out, sectionStyle.Render(" "+truncate(r.label, w-3)))
		case i == m.proj.rowCur:
			out = append(out, selectedItemStyle.Render(" ❯ "+truncate(r.label, w-6)))
		default:
			lw := w - 6
			if r.missing {
				lw = w - 16
			}
			line := "  " + itemStyle.Render(truncate(r.label, lw))
			if r.missing {
				line += mutedStyle.Render(" — missing")
			}
			out = append(out, line)
		}
	}
	return clipBlock(strings.Join(out, "\n"), pane)
}

// chatContent renders the whole transcript wrapped to w. Styles are applied
// per wrapped segment so wrapping can never cut an escape code in half.
func (m Model) chatContent(w int) string {
	s := m.proj.session
	if s == nil || s.Asker == nil {
		return ""
	}
	lines, _, _ := s.Asker.Snapshot()
	var out []string
	for _, ln := range lines {
		out = append(out, chatLine(ln, w)...)
	}
	return strings.Join(out, "\n")
}

// chatLine styles one transcript entry by role: agent in plain text, system
// muted, errors red, engine log dim, the human's answers accented.
func chatLine(ln Line, w int) []string {
	pref := ""
	switch ln.Role {
	case "user":
		pref = "❯ "
	case "system":
		pref = "· "
	case "error":
		pref = "✖ "
	case "log":
		pref = "  "
	}
	styled := func(s string) string {
		switch ln.Role {
		case "user":
			return accentStyle.Render(s)
		case "system", "log":
			return mutedStyle.Render(s)
		case "error":
			return badStyle.Render(s)
		default:
			return s
		}
	}
	indent := strings.Repeat(" ", lipgloss.Width(pref))
	segs := wrapLines(ln.Text, max(4, w-lipgloss.Width(pref)))
	out := make([]string, 0, len(segs))
	for i, seg := range segs {
		if i == 0 {
			out = append(out, styled(pref+seg))
			continue
		}
		out = append(out, styled(indent+seg))
	}
	if len(out) == 0 {
		out = append(out, styled(pref))
	}
	return out
}

// outcomeText colours the run's verdict the way the web report does.
func outcomeText(outcome, phase string, w int) string {
	raw, st := outcome, mutedStyle
	switch {
	case outcome == "":
		raw = "in progress"
		if phase == state.PhaseSpec {
			raw = "awaiting spec"
		}
	case outcome == "complete":
		raw, st = "✔ complete", okStyle
	case outcome == "finished-with-issues":
		raw, st = "⚠ finished with issues", warnStyle
	}
	return st.Render(truncate(raw, fitW(w)))
}

// taskBar is a compact progress strip; a project with no tasks draws none.
func taskBar(done, total, w int) string {
	if total <= 0 || w <= 0 {
		return ""
	}
	filled := clamp(done*w/total, 0, w)
	return okStyle.Render(strings.Repeat("█", filled)) +
		mutedStyle.Render(strings.Repeat("░", w-filled))
}

// labelCol is the fixed-width muted label a detail row hangs off.
func labelCol(label string) string {
	return "  " + mutedStyle.Render(fmt.Sprintf("%-11s", label))
}

// fitW is the width left for a value next to a labelCol.
func fitW(w int) int { return max(6, w-16) }

// wrapToWidth word-wraps a whole block to w columns.
func wrapToWidth(s string, w int) string {
	return strings.Join(wrapLines(s, w), "\n")
}

// wrapLines word-wraps each logical line of s to w, hard-breaking words that
// are longer than the pane. It counts runes, the same currency truncate uses.
// Tabs become four spaces so log columns stay roughly aligned.
func wrapLines(s string, w int) []string {
	if w < 4 {
		w = 4
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\t", "    "), "\n") {
		runes := []rune(para)
		for len(runes) > w {
			cut := -1 // break at the last space inside the window, if any
			for i := w; i > 0; i-- {
				if runes[i] == ' ' {
					cut = i
					break
				}
			}
			if cut < 0 {
				cut = w
			}
			out = append(out, string(runes[:cut]))
			rest := runes[cut:]
			for len(rest) > 0 && rest[0] == ' ' {
				rest = rest[1:]
			}
			runes = rest
		}
		out = append(out, string(runes))
	}
	return out
}

// clipBlock keeps a rendered block to n whole lines so no screen can push the
// frame past the terminal height. Widths are handled by truncating raw text
// before styling — cutting styled lines here would corrupt escape codes.
func clipBlock(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n], "\n")
}

// ---- tasks tab ----

// taskRetryMsg is retryTaskCmd's verdict.
type taskRetryMsg struct {
	id       string
	released []string
	err      error
}

// retryTaskCmd re-queues a blocked task through the same app operation the
// CLI and the web interface use, so all three behave identically.
func retryTaskCmd(pr *app.Project, id string) tea.Cmd {
	return func() tea.Msg {
		released, err := pr.RetryTask(id)
		return taskRetryMsg{id: id, released: released, err: err}
	}
}

// firstBlockedTask lands the cursor on something worth looking at.
func firstBlockedTask(tasks []*state.Task) int {
	for i, t := range tasks {
		if t.Status == state.TaskBlocked {
			return i
		}
	}
	return 0
}

func taskMarker(status string) string {
	switch status {
	case state.TaskDone:
		return "✔"
	case state.TaskActive:
		return "▶"
	case state.TaskBlocked:
		return "✖"
	default:
		return "·"
	}
}

func (m Model) tasksKey(msg tea.KeyMsg, key string) (tea.Model, tea.Cmd) {
	st := m.proj.st
	if st == nil || len(st.Tasks) == 0 {
		return m, nil
	}
	switch key {
	case "up", "k":
		if m.proj.taskSel > 0 {
			m.proj.taskSel--
		}
		m.proj.taskConfirm = ""
	case "down", "j":
		if m.proj.taskSel < len(st.Tasks)-1 {
			m.proj.taskSel++
		}
		m.proj.taskConfirm = ""
	case "u":
		if m.proj.taskSel >= len(st.Tasks) {
			return m, nil
		}
		t := st.Tasks[m.proj.taskSel]
		if t.Status != state.TaskBlocked {
			return m, sayStatus(t.ID+" is "+t.Status+" — re-queue applies to blocked tasks", true)
		}
		// Re-queueing resets an attempt counter: two presses, like delete.
		if m.proj.taskConfirm != t.ID {
			m.proj.taskConfirm = t.ID
			return m, sayStatus(
				"re-queue "+t.ID+"? press u again — attempts reset, dependents released, a finished project reopens", true)
		}
		m.proj.taskConfirm = ""
		return m, retryTaskCmd(m.proj.pr, t.ID)
	}
	return m, nil
}

// viewTasks is the terminal half of "what actually went wrong": every task
// with its status, and the selected task's recorded failure.
func (m Model) viewTasks(w, pane int) string {
	st := m.proj.st
	if st == nil || len(st.Tasks) == 0 {
		return clipBlock(mutedStyle.Render("  No tasks yet — planning creates them when the build starts."), pane)
	}
	if m.proj.taskSel >= len(st.Tasks) {
		m.proj.taskSel = len(st.Tasks) - 1
	}

	done, blocked, total := st.Counts()
	lines := []string{
		titleStyle.Render(fmt.Sprintf("Tasks — %d/%d done", done, total)) +
			func() string {
				if blocked > 0 {
					return warnStyle.Render(fmt.Sprintf("  %d blocked", blocked))
				}
				return ""
			}(),
		"",
	}
	for i, t := range st.Tasks {
		if len(lines) >= pane-6 {
			break
		}
		row := fmt.Sprintf("%s %-7s %-12s att=%d  %s", taskMarker(t.Status), t.ID, t.Status, t.Attempts, t.Title)
		if i == m.proj.taskSel {
			lines = append(lines, selectedItemStyle.Width(max(10, w-2)).Render(truncate("❯ "+row, w-3)))
		} else {
			lines = append(lines, "  "+truncate(row, w-3))
		}
	}

	// Detail for the selection: the reason it stopped, verbatim.
	if m.proj.taskSel >= len(st.Tasks) {
		return clipBlock(strings.Join(lines, "\n"), pane)
	}
	t := st.Tasks[m.proj.taskSel]
	detail := []string{"", sectionStyle.Render(t.ID + " · " + t.Title)}
	if len(t.DependsOn) > 0 {
		detail = append(detail, mutedStyle.Render("depends on "+strings.Join(t.DependsOn, ", ")))
	}
	if t.Commit != "" {
		detail = append(detail, mutedStyle.Render("commit "+t.Commit))
	}
	if t.Status == state.TaskBlocked {
		if len(t.Notes) > 0 {
			detail = append(detail, badStyle.Render(truncate(t.Notes[len(t.Notes)-1], w-3)))
		}
		detail = append(detail, accentStyle.Render("press u twice to re-queue this task"))
	} else if t.LastFeedback != "" {
		detail = append(detail, mutedStyle.Render(truncate(t.LastFeedback, w-3)))
	}
	lines = append(lines, detail...)
	return clipBlock(strings.Join(lines, "\n"), pane)
}
