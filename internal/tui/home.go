package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/app"
)

// homeState holds the (currently trivial) state of the projects screen.
type homeState struct{}

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
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(items)-1 {
			m.cursor++
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = max(0, len(items)-1)
	case "/":
		m.filterOn = true
		m.filter = ""
		return m, nil
	case "enter", "l":
		if p := m.current(); p != nil {
			return m, m.openProject(*p)
		}
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
