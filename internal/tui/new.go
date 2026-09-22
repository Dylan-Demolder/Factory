package tui

import (
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/config"
)

// newProjectState is the create form: name and idea. The idea is what the
// interview is built from, so the placeholder teaches rather than nags.
type newProjectState struct {
	name     textinput.Model
	idea     textinput.Model
	focus    int // 0 = name, 1 = idea
	inited   bool
	creating bool
}

// init creates the inputs once; calling it again is a no-op so that arriving
// on the screen by any route behaves the same.
func (s *newProjectState) init() {
	if s.inited {
		return
	}
	s.name = textinput.New()
	s.name.Placeholder = "notes-cli"
	s.name.Prompt = accentStyle.Render("› ")
	s.name.CharLimit = 64
	s.name.Width = 40

	s.idea = textinput.New()
	s.idea.Placeholder = "Markdown notes with tags and full-text search"
	s.idea.Prompt = accentStyle.Render("› ")
	s.idea.Width = 60

	s.focus = 0
	s.name.Focus()
	s.inited = true
}

// typing reports that this screen owns the keyboard, so global shortcuts such
// as "q" or "1-4" reach the form instead of navigating away mid-answer.
func (s newProjectState) typing() bool { return s.inited }

func (m Model) updateNew(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.fresh.init()

	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc":
		m.route = routeHome
		return m, loadProjects(m.Workspace)
	case "tab", "shift+tab":
		// Two fields, so both directions land on the other one.
		m.fresh.focus = (m.fresh.focus + 1) % 2
		if m.fresh.focus == 0 {
			m.fresh.idea.Blur()
			return m, m.fresh.name.Focus()
		}
		m.fresh.name.Blur()
		return m, m.fresh.idea.Focus()
	case "ctrl+n":
		if !m.fresh.creating {
			return m, m.createProject()
		}
	case "enter":
		if strings.TrimSpace(m.fresh.name.Value()) == "" {
			// A project without a name is not a project; nudge, do not fail.
			m.status = status{text: "give the project a name first", bad: true}
			m.fresh.focus = 0
			return m, m.fresh.name.Focus()
		}
		if !m.fresh.creating {
			return m, m.createProject()
		}
	}

	var cmd tea.Cmd
	if m.fresh.focus == 0 {
		m.fresh.name, cmd = m.fresh.name.Update(km)
	} else {
		m.fresh.idea, cmd = m.fresh.idea.Update(km)
	}
	return m, cmd
}

// createProject makes the project directory, snapshots the config into it and
// hands back a project ready for its interview. The snapshot means later
// config edits cannot change a build that is already running.
func (m Model) createProject() tea.Cmd {
	name := strings.TrimSpace(m.fresh.name.Value())
	idea := strings.TrimSpace(m.fresh.idea.Value())
	workspace, cfgFlag := m.Workspace, m.CfgPath
	return func() tea.Msg {
		if err := app.ValidName(name); err != nil {
			return projectLoadedMsg{err: err}
		}
		cfg, cfgPath, err := config.Resolve(cfgFlag, "")
		if err != nil {
			return projectLoadedMsg{err: err}
		}
		dir := filepath.Join(workspace, name)
		pr, err := app.Create(dir, name, idea, cfg, cfgPath)
		if err != nil {
			return projectLoadedMsg{err: err}
		}
		sum, _ := app.Summarize(filepath.Base(dir), dir)
		return projectLoadedMsg{pr: pr, sum: sum, startInterview: true}
	}
}

func (m Model) viewNew(w, h int) string {
	m.fresh.ensureDrawn()
	var b strings.Builder
	b.WriteString(h1Style.Render("New project") + "\n\n")
	b.WriteString(mutedStyle.Render("factory will interview you until the idea can be built,") + "\n")
	b.WriteString(mutedStyle.Render("then your agents take it from there.") + "\n\n")

	nameRow := "  " + accentStyle.Render("name") + "\n  " + m.fresh.name.View()
	ideaRow := "  " + accentStyle.Render("idea") + "\n  " + m.fresh.idea.View()

	card := cardStyle.Render(nameRow + "\n\n" + ideaRow)
	b.WriteString(card)
	b.WriteString("\n\n  " + mutedStyle.Render("enter next · ctrl+n create · esc back"))

	if m.fresh.creating {
		b.WriteString("\n\n  " + m.spinner.View() + " creating…")
	}
	return lipgloss.NewStyle().Padding(1, 2).Width(w).Height(h).Render(b.String())
}

// ensureDrawn makes sure inputs exist before the first draw, because View
// runs before any Update that could have initialised them.
func (s *newProjectState) ensureDrawn() { s.init() }
