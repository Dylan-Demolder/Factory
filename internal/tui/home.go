package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/app"
)

// updateHome handles keys on the projects screen. Anything that is not a key
// press (spinners, refreshes) leaves the screen untouched.
func (m Model) updateHome(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	items := m.visibleProjects()
	switch km.String() {
	case "up", "k":
		// Moving off a row abandons any pending delete for the old one.
		m.delConfirm = ""
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		m.delConfirm = ""
		if m.cursor < len(items)-1 {
			m.cursor++
		}
	case "g":
		m.delConfirm = ""
		m.cursor = 0
	case "G":
		m.delConfirm = ""
		m.cursor = max(0, len(items)-1)
	case "/":
		m.filterOn = true
		m.filter = ""
		return m, nil
	case "enter", "l":
		if p := m.current(); p != nil {
			return m, m.openProject(*p)
		}
	case "d":
		p := m.current()
		if p == nil {
			return m, sayStatus("select a project to delete", true)
		}
		if m.delConfirm == p.ID {
			m.delConfirm = ""
			return m, deleteProject(*p)
		}
		m.delConfirm = p.ID
		running := ""
		if p.Running {
			running = " and stop its running build"
		}
		return m, sayStatus(
			fmt.Sprintf("delete %q%s? press d again to confirm — esc cancels", p.Name, running), true)
	case "n":
		m.route = routeNew
		m.fresh = newProjectState{}
		m.fresh.init()
		return m, nil
	case "R":
		return m, tea.Batch(loadProjects(m.Workspace), sayStatus("reloading", false))
	}
	if m.filterOn {
		return m, nil
	}
	return m, nil
}

// updateFilterKeys handles typing once the sidebar filter is open. The caller
// has already established that filtering is active.
func (m Model) updateFilterKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.filterOn = false
		if msg.String() == "esc" {
			m.filter = ""
		}
	case "backspace":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
		}
	default:
		if len(msg.String()) == 1 && msg.String() >= " " {
			m.filter += msg.String()
		}
	}
	if m.cursor >= len(m.visibleProjects()) {
		m.cursor = max(0, len(m.visibleProjects())-1)
	}
	return m, nil
}

// renderMain dispatches to the visible screen. It must stay pure: all state
// changes happen in Update, never while drawing.
func (m Model) renderMain(w, h int) string {
	switch m.route {
	case routeNew:
		return m.viewNew(w, h)
	case routeProject:
		return m.viewProject(w, h)
	case routeOrg:
		return m.viewOrg(w, h)
	case routeDoctor:
		return m.viewDoctor(w, h)
	default:
		return m.viewHome(w, h)
	}
}

// viewHome is the landing screen: what is running, and what to do next.
func (m Model) viewHome(w, h int) string {
	var b strings.Builder
	b.WriteString(h1Style.Render("Welcome to factory") + "\n\n")
	b.WriteString(mutedStyle.Render("Submit an idea, spec it together, then let your agents build it.") + "\n\n")

	// Active builds first — this screen's job is to answer "what is happening?"
	var running []app.Summary
	for _, p := range m.projects {
		if p.Running {
			running = append(running, p)
		}
	}
	if len(running) > 0 {
		b.WriteString(sectionStyle.Render("Running now") + "\n")
		for _, p := range running {
			b.WriteString("  " + phasePill(p.Phase, true) + "  " + titleStyle.Render(p.Name) + "\n")
			b.WriteString("    " + mutedStyle.Render(fmt.Sprintf("pid %d · updated %s", p.Pid, started(p.Updated))) + "\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(sectionStyle.Render("All projects") + "\n")
	if len(m.projects) == 0 {
		b.WriteString("\n  " + mutedStyle.Render("Nothing here yet.") + "\n\n")
		b.WriteString("  " + accentStyle.Render("2") + mutedStyle.Render("  create a project") + "\n")
		b.WriteString("  " + accentStyle.Render("ctrl+k") + mutedStyle.Render("  command palette") + "\n")
	}
	for _, p := range m.projects {
		if b.Len() > 4000 {
			break
		}
		b.WriteString("  " + phasePill(p.Phase, p.Running) + "  " + titleStyle.Render(p.Name))
		if p.Total > 0 {
			b.WriteString(mutedStyle.Render(fmt.Sprintf("  %d/%d tasks", p.Done, p.Total)))
		}
		if p.Blocked > 0 {
			b.WriteString("  " + warnStyle.Render(fmt.Sprintf("%d blocked", p.Blocked)))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n" + sectionStyle.Render("Workspace") + "\n")
	b.WriteString("  " + mutedStyle.Render(m.Workspace) + "\n")
	return lipgloss.NewStyle().Padding(1, 2).Width(w).Height(h).Render(b.String())
}

// projectDeletedMsg is deleteProject's verdict.
type projectDeletedMsg struct {
	name string
	err  error
}

// deleteProject stops a running build first, then removes the directory.
//
// Stopping matters twice over: app.Delete refuses a running build, and
// deleting out from under a live `factory run` would leave it writing into a
// hole. StopAndWait (rather than Stop) because Terminate only signals.
func deleteProject(p app.Summary) tea.Cmd {
	return func() tea.Msg {
		pr, err := app.Open(p.Dir, "")
		if err != nil {
			return projectDeletedMsg{name: p.Name,
				err: fmt.Errorf("%s is not a factory project", p.Dir)}
		}
		if _, running := pr.Running(); running {
			if err := pr.StopAndWait(10 * time.Second); err != nil {
				return projectDeletedMsg{name: p.Name, err: err}
			}
		}
		if err := app.Delete(p.Dir); err != nil {
			return projectDeletedMsg{name: p.Name, err: err}
		}
		return projectDeletedMsg{name: p.Name}
	}
}
