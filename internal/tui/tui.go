// Package tui is factory's interactive terminal workspace: the hands-on side
// of the product, where you run the spec interview, drive builds and shape the
// org chart. The web interface is its informational counterpart — both work
// against the same plain files on disk, so neither needs the other running.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/app"
)

type route int

const (
	routeHome route = iota
	routeNew
	routeProject
	routeOrg
	routeDoctor
)

func (r route) String() string {
	switch r {
	case routeNew:
		return "New project"
	case routeProject:
		return "Project"
	case routeOrg:
		return "Org chart"
	case routeDoctor:
		return "Doctor"
	default:
		return "Projects"
	}
}

// status is the one-line message under the title; bad turns it red.
type status struct {
	text string
	bad  bool
	at   time.Time
}

// Model is the whole application. Screens keep their own state in the fields
// below and implement update<View>/view<View> in their own file.
type Model struct {
	Workspace string
	CfgPath   string

	width, height int

	route    route
	projects []app.Summary
	cursor   int
	filter   string
	filterOn bool
	loading  bool
	err      error
	status   status
	showHelp bool
	spinner  spinner.Model

	// screen state
	home  homeState
	fresh newProjectState
	proj  projectState
	org   orgState
	doc   doctorState

	pal *palette

	// send delivers a message back into the program from another goroutine
	// (the interview runs on one). Guarded: it is nil until Run wires it up.
	send func(tea.Msg)
}

// setRouteMsg switches screens from an action. Palette callbacks run over a
// copy of the model, so they return a message instead of mutating in place —
// mutating the copy would silently do nothing.
type setRouteMsg struct{ to route }

func setRoute(to route) tea.Cmd {
	return func() tea.Msg { return setRouteMsg{to: to} }
}

// NewModel builds the initial model.
func NewModel(workspace, cfgPath string) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(colorAccent)
	return Model{
		Workspace: workspace,
		CfgPath:   cfgPath,
		route:     routeHome,
		loading:   true,
		spinner:   sp,
	}
}

// Init starts loading projects and the refresh heartbeat. The heartbeat is
// owned here, not by any screen, so visiting another screen cannot leave the
// projects view stale.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, loadProjects(m.Workspace), tick())
}

type projectsMsg struct {
	items []app.Summary
	err   error
}

func loadProjects(workspace string) tea.Cmd {
	return func() tea.Msg {
		items, err := app.List(workspace)
		return projectsMsg{items: items, err: err}
	}
}

type statusMsg struct {
	text string
	bad  bool
}

func sayStatus(text string, bad bool) tea.Cmd {
	return func() tea.Msg { return statusMsg{text: text, bad: bad} }
}

// tick periodically refreshes project state so running builds, phase changes
// and log growth show up without the user pressing anything.
const tickEvery = 2 * time.Second

func tick() tea.Cmd {
	return tea.Tick(tickEvery, func(time.Time) tea.Msg { return refreshMsg{} })
}

type refreshMsg struct{}

// ---- update ----

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case refreshMsg:
		// Re-arm first: the heartbeat must survive screens that have nothing
		// to refresh, or returning to the projects view would be frozen.
		cmds := []tea.Cmd{tick()}
		if m.route == routeHome || m.route == routeProject {
			cmds = append(cmds, loadProjects(m.Workspace))
			if m.route == routeProject {
				cmds = append(cmds, m.projectRefresh())
			}
		}
		return m, tea.Batch(cmds...)

	case projectsMsg:
		if msg.err != nil {
			m.err = msg.err
			m.loading = false
			return m, nil
		}
		m.projects = msg.items
		m.loading = false
		m.err = nil
		if m.cursor >= len(m.projects) {
			m.cursor = max(0, len(m.projects)-1)
		}
		return m, nil

	case statusMsg:
		m.status = status{text: msg.text, bad: msg.bad, at: time.Now()}
		return m, nil

	case setRouteMsg:
		// Navigating away from an org chart with unsaved edits would silently
		// discard them, so refuse and say so instead.
		if m.route == routeOrg && m.org.typing() {
			m.status = status{text: "unsaved changes — s to save, esc to discard", bad: true, at: time.Now()}
			return m, nil
		}
		m.route = msg.to
		var cmds []tea.Cmd
		switch msg.to {
		case routeHome, routeProject:
			cmds = append(cmds, loadProjects(m.Workspace))
		case routeNew:
			m.fresh = newProjectState{}
			m.fresh.init()
		case routeOrg:
			cmds = append(cmds, m.loadOrg())
		case routeDoctor:
			cmds = append(cmds, m.runDoctor())
		}
		return m, tea.Batch(cmds...)

	case projectLoadedMsg:
		if msg.err != nil {
			m.status = status{text: msg.err.Error(), bad: true, at: time.Now()}
			return m, nil
		}
		m.route = routeProject
		cmd := m.loadProject(msg.pr, msg.sum)
		if msg.startInterview {
			cmd = tea.Batch(cmd, m.startInterview(msg.pr))
		}
		return m, tea.Batch(cmd, loadProjects(m.Workspace))

	case refreshInterviewMsg:
		return m, nil // the transcript changed; redraw with current state

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		// The sidebar filter owns keystrokes while it is open.
		if m.filterOn {
			return m.updateFilterKeys(msg)
		}
		// The palette swallows input while it is open.
		if m.pal != nil {
			return m.updatePalette(msg)
		}
		// ctrl+k opens the command palette from anywhere.
		if msg.String() == "ctrl+k" {
			m.pal = newPalette(m.actions())
			return m, nil
		}
		// ? toggles the key legend, unless a screen is typing.
		if msg.String() == "?" && !m.typing() {
			m.showHelp = !m.showHelp
			return m, nil
		}
		// esc backs out one level at a time: a project tab first, then the
		// project, then the list — jumping straight out of a file you were
		// reading would be disorienting.
		if msg.String() == "esc" && !m.typing() {
			if m.route == routeProject && m.proj.tab != tabOverview {
				m.proj.tab = tabOverview
				return m, nil
			}
			if m.route != routeHome {
				m.route = routeHome
				return m, loadProjects(m.Workspace)
			}
		}
		// 1-4 jump between screens when nothing is focused.
		if !m.typing() && !m.filterOn {
			switch msg.String() {
			case "1":
				m.route, m.filterOn, m.filter = routeHome, false, ""
				return m, loadProjects(m.Workspace)
			case "2":
				m.route, m.filterOn, m.filter = routeNew, false, ""
				m.fresh = newProjectState{}
				m.fresh.init()
				return m, nil
			case "3":
				m.route, m.filterOn, m.filter = routeOrg, false, ""
				return m, m.loadOrg()
			case "4":
				m.route, m.filterOn, m.filter = routeDoctor, false, ""
				return m, nil
			case "q":
				return m, tea.Quit
			}
		}
		return m.routeUpdate(msg)
	}
	return m.routeUpdate(msg)
}

// routeUpdate hands the message to whichever screen is showing. Global keys
// are handled above; each screen owns the rest.
func (m Model) routeUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.route {
	case routeNew:
		return m.updateNew(msg)
	case routeProject:
		return m.updateProject(msg)
	case routeOrg:
		return m.updateOrg(msg)
	case routeDoctor:
		return m.updateDoctor(msg)
	default:
		return m.updateHome(msg)
	}
}

// typing reports whether the focused screen is collecting text, so global
// shortcuts do not steal keystrokes mid-answer.
func (m Model) typing() bool {
	switch m.route {
	case routeNew:
		return m.fresh.typing()
	case routeProject:
		return m.proj.typing()
	case routeOrg:
		// True while an inline editor is open, a confirm is pending, or there
		// are unsaved edits — root must not steal esc/1-4/q from any of them.
		return m.org.typing()
	default:
		return false
	}
}

// ---- view ----

func (m Model) View() string {
	if m.width == 0 {
		return "starting…"
	}
	sidebarW := clamp(m.width*26/100, 22, 34)
	mainW := m.width - sidebarW - 1 // the right border sits outside the sidebar's width

	// The palette draws its own full-screen canvas.
	if m.pal != nil {
		return m.renderPalette()
	}

	// Frame the screen exactly: top bar + its border, the status border +
	// status line, and the optional key legend. Four lines of chrome — using
	// three (as if only one of the two bordered rows existed) overflowed the
	// terminal by a line and scrolled the brand row out of view.
	legend := ""
	legendH := 0
	if m.showHelp {
		legend = m.keyLegend()
		legendH = lipgloss.Height(legend) + 1
	}
	bodyH := m.height - 4 - legendH
	if bodyH < 3 {
		bodyH = 3
	}

	sidebar := m.renderSidebar(sidebarW, bodyH)
	main := m.renderMain(mainW, bodyH)

	// Pad by measured width, not by an estimate: an over-long row wraps in
	// the terminal and pushes the whole frame down a line.
	logo := logoStyle.Render("◆ factory")
	routeLabel := routeStyle.Render(m.route.String())
	topPad := max(1, m.width-lipgloss.Width(logo)-lipgloss.Width(routeLabel))
	top := topbarStyle.Width(m.width).Render(logo + strings.Repeat(" ", topPad) + routeLabel)

	helpLine := m.helpLine()
	statusLeft, statusRight := m.statusText(), helpLine
	inner := m.width - 2 // statusStyle carries padding(0, 1)
	statusPad := max(1, inner-lipgloss.Width(statusLeft)-lipgloss.Width(statusRight))
	bottom := statusStyle.Width(m.width).Render(statusLeft + strings.Repeat(" ", statusPad) + statusRight)

	out := top + "\n" +
		lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main) + "\n" +
		bottom
	if legend != "" {
		out += "\n" + legend
	}
	return out
}

func (m Model) renderSidebar(w, h int) string {
	lines := []string{sectionStyle.Width(w - 2).Render("Projects")}
	if m.loading {
		lines = append(lines, mutedStyle.Render("  "+m.spinner.View()+" loading…"))
	} else if m.err != nil {
		lines = append(lines, badStyle.Render("  "+truncate(m.err.Error(), w-4)))
	} else if len(m.visibleProjects()) == 0 {
		lines = append(lines, mutedStyle.Render("  No projects yet."),
			mutedStyle.Render("  Press 2 or ctrl+k"),
			mutedStyle.Render("  to create one."))
	}
	for i, p := range m.visibleProjects() {
		if len(lines) >= h-1 {
			lines = append(lines, mutedStyle.Render("  …"))
			break
		}
		style := itemStyle.Width(w - 2)
		if i == m.cursor && !m.filterOn {
			style = selectedItemStyle.Width(w - 2)
		}
		name := p.Name
		if p.Running {
			name += " " + runningDot
		}
		lines = append(lines, style.Render(truncate(name, w-5)))
		if i == m.cursor && !m.filterOn {
			lines = append(lines, mutedStyle.Width(w-2).Render(
				"  "+phaseLabel(p)+" "+progressText(p)))
		}
	}
	body := strings.Join(lines, "\n")
	if m.filterOn {
		body += "\n" + filterStyle.Width(w-2).Render("/"+m.filter+"▏")
	}
	return sidebarStyle.Width(w).Height(h).Render(body)
}

func (m Model) statusText() string {
	if m.status.text != "" && time.Since(m.status.at) < 12*time.Second {
		if m.status.bad {
			return badStyle.Render("✖ " + m.status.text)
		}
		return okStyle.Render("✔ " + m.status.text)
	}
	if m.loading {
		return mutedStyle.Render(m.spinner.View() + " working…")
	}
	if len(m.projects) > 0 {
		running := 0
		for _, p := range m.projects {
			if p.Running {
				running++
			}
		}
		if running > 0 {
			return accentStyle.Render(fmt.Sprintf("%d build(s) running", running))
		}
	}
	return mutedStyle.Render(m.Workspace)
}

func (m Model) helpLine() string {
	return helpStyle.Render(m.keysForRoute())
}

func (m Model) keysForRoute() string {
	if m.pal != nil {
		return "↑↓ move · ⏎ run · esc close"
	}
	base := "ctrl+k palette · 1-4 screens · ? keys · q quit"
	if m.filterOn {
		return "type to filter · ⏎ done · esc cancel"
	}
	switch m.route {
	case routeProject:
		return "⏎ open tab · tab tabs · esc back · " + base
	case routeNew:
		return "⏎ next · esc back · " + base
	case routeOrg:
		return "↑↓ seat · ←→ move · ⏎ edit · " + base
	case routeDoctor:
		return "⏎ check all · esc back · " + base
	default:
		return "↑↓ select · ⏎ open · / filter · " + base
	}
}

func (m Model) keyLegend() string {
	rows := []string{
		"↑ ↓ j k    move selection",
		"⏎          open / confirm",
		"/          filter projects",
		"ctrl+k     command palette",
		"1 2 3 4    projects · new · org · doctor",
		"esc        back      q quit",
	}
	box := lipgloss.NewStyle().Foreground(colorText).Padding(0, 2).Render(strings.Join(rows, "\n"))
	return helpBoxStyle.Width(min(m.width, 46)).Render(box)
}

// visibleProjects applies the sidebar filter.
func (m Model) visibleProjects() []app.Summary {
	if m.filter == "" {
		return m.projects
	}
	needle := strings.ToLower(m.filter)
	out := make([]app.Summary, 0, len(m.projects))
	for _, p := range m.projects {
		if strings.Contains(strings.ToLower(p.Name), needle) {
			out = append(out, p)
		}
	}
	return out
}

// current returns the highlighted project, if any.
func (m Model) current() *app.Summary {
	items := m.visibleProjects()
	if m.cursor < 0 || m.cursor >= len(items) {
		return nil
	}
	return &items[m.cursor]
}

// ---- palette ----

type action struct {
	title string
	hint  string
	run   func() tea.Cmd
}

type palette struct {
	items  []action
	sel    int
	filter string
}

func newPalette(items []action) *palette { return &palette{items: items} }

func (p *palette) matches() []action {
	if p.filter == "" {
		return p.items
	}
	needle := strings.ToLower(p.filter)
	var out []action
	for _, a := range p.items {
		if strings.Contains(strings.ToLower(a.title), needle) {
			out = append(out, a)
		}
	}
	return out
}

func (m *Model) updatePalette(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.pal
	switch msg.String() {
	case "esc", "ctrl+k":
		m.pal = nil
		return *m, nil
	case "up", "ctrl+p":
		if p.sel > 0 {
			p.sel--
		}
		return *m, nil
	case "down", "ctrl+n":
		if p.sel < len(p.matches())-1 {
			p.sel++
		}
		return *m, nil
	case "backspace":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len(p.filter)-1]
			p.sel = 0
		}
		return *m, nil
	case "enter":
		items := p.matches()
		if p.sel >= 0 && p.sel < len(items) {
			run := items[p.sel].run
			m.pal = nil
			if run != nil {
				return *m, run()
			}
		}
		return *m, nil
	}
	// Printable characters filter the list.
	if len(msg.String()) == 1 && msg.String() >= " " {
		p.filter += msg.String()
		p.sel = 0
	}
	return *m, nil
}

// renderPalette draws the command palette as its own canvas. Overlaying it on
// the frame would add rows and push the layout past the terminal height, so
// while it is open it owns the screen.
func (m Model) renderPalette() string {
	items := m.pal.matches()
	const shown = 8
	rows := []string{paletteStyle.Render("  " + paletteInput(m.pal.filter))}
	if len(items) == 0 {
		rows = append(rows, mutedStyle.Render("  no matching action"))
	}
	start := max(0, min(m.pal.sel-shown/2, len(items)-shown))
	for i := start; i < len(items) && i < start+shown; i++ {
		a := items[i]
		if i == m.pal.sel {
			rows = append(rows,
				paletteSelStyle.Render("\u276f "+a.title)+mutedStyle.Render("  "+a.hint))
			continue
		}
		line := "  " + a.title
		if a.hint != "" {
			line += mutedStyle.Render("  " + a.hint)
		}
		rows = append(rows, line)
	}
	width := clamp(m.width-10, 30, 62)
	box := paletteBoxStyle.Width(width).Render(strings.Join(rows, "\n"))
	boxW, boxH := lipgloss.Width(box), lipgloss.Height(box)

	hint := mutedStyle.Render("\u2191\u2193 move \u00b7 \u23ce run \u00b7 ctrl+k or esc to close")
	left := max(0, (m.width-boxW)/2)
	hintLeft := max(0, (m.width-lipgloss.Width(hint))/2)
	top := max(0, (m.height-boxH-2)/2)

	canvas := make([]string, 0, m.height)
	for len(canvas) < top {
		canvas = append(canvas, "")
	}
	for _, line := range strings.Split(box, "\n") {
		canvas = append(canvas, strings.Repeat(" ", left)+line)
	}
	canvas = append(canvas, "", strings.Repeat(" ", hintLeft)+hint)
	for len(canvas) < m.height {
		canvas = append(canvas, "")
	}
	// Exactly the terminal height: one row more and the terminal scrolls.
	if len(canvas) > m.height {
		canvas = canvas[:m.height]
	}
	return strings.Join(canvas, "\n")
}

// actions is the palette's command registry. Each entry returns a Cmd: the
// closure sees a copy of the model, so nothing may be mutated directly here.
func (m Model) actions() []action {
	items := []action{
		{title: "New project", hint: "interview a fresh idea", run: func() tea.Cmd {
			return setRoute(routeNew)
		}},
		{title: "Org chart", hint: "agents, roles and seats", run: func() tea.Cmd {
			return tea.Batch(setRoute(routeOrg), m.loadOrg())
		}},
		{title: "Doctor", hint: "check every agent answers", run: func() tea.Cmd {
			return tea.Batch(setRoute(routeDoctor), m.runDoctor())
		}},
		{title: "Reload projects", run: func() tea.Cmd { return loadProjects(m.Workspace) }},
		{title: "Quit", run: func() tea.Cmd { return tea.Quit }},
	}
	for _, p := range m.projects {
		p := p
		items = append(items, action{
			title: "Open " + p.Name,
			hint:  p.Phase,
			run:   func() tea.Cmd { return m.openProject(p) },
		})
	}
	return items
}

// ---- helpers shared by screens ----

func phaseLabel(p app.Summary) string {
	if p.Running {
		return runningDot + " " + p.Phase
	}
	return p.Phase
}

func progressText(p app.Summary) string {
	if p.Total == 0 {
		return ""
	}
	return fmt.Sprintf("%d/%d", p.Done, p.Total)
}

const runningDot = "●"

func truncate(s string, n int) string {
	if n <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func paletteInput(filter string) string {
	if filter == "" {
		return mutedStyle.Render("Type a command…▏")
	}
	return filter + "▏"
}

// Run starts the terminal workspace. It owns the terminal for as long as it
// runs and restores it on return.
func Run(workspace, cfgPath, version string) error {
	var p *tea.Program
	m := NewModel(workspace, cfgPath)
	// The interview runs on its own goroutine; this is how it asks for a
	// redraw. p is read through the closure, so wiring it before NewProgram
	// returns is safe.
	m.send = func(msg tea.Msg) {
		if p != nil {
			p.Send(msg)
		}
	}
	p = tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
