package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/app"
)

func TestTruncateAndClamp(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("truncate short = %q", got)
	}
	if got := truncate("hello world", 8); got != "hello w…" {
		t.Errorf("truncate = %q, want %q", got, "hello w…")
	}
	if got := truncate("hello", 1); got != "…" {
		t.Errorf("truncate to 1 = %q", got)
	}
	if clamp(5, 10, 20) != 10 || clamp(50, 10, 20) != 20 || clamp(15, 10, 20) != 15 {
		t.Error("clamp out of range")
	}
	if min(2, 5) != 2 || max(2, 5) != 5 {
		t.Error("min/max")
	}
}

func TestProgressTextHandlesEmptyProject(t *testing.T) {
	if got := progressText(app.Summary{Done: 0, Total: 0}); got != "" {
		t.Errorf("progress with no tasks = %q, want empty", got)
	}
	if got := progressText(app.Summary{Done: 3, Total: 5}); got != "3/5" {
		t.Errorf("progress = %q", got)
	}
}

func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
func ctrlK() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyCtrlK} }
func esc() tea.KeyMsg       { return tea.KeyMsg{Type: tea.KeyEsc} }
func enter() tea.KeyMsg     { return tea.KeyMsg{Type: tea.KeyEnter} }

func step(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want Model", next)
	}
	return got
}

func TestScreenKeysAndQuit(t *testing.T) {
	base := NewModel("/tmp/ws", "")
	base = step(t, base, tea.WindowSizeMsg{Width: 100, Height: 30})

	cases := map[rune]route{
		'1': routeHome,
		'2': routeNew,
		'3': routeOrg,
		'4': routeDoctor,
	}
	for r, want := range cases {
		m := step(t, base, key(r))
		if m.route != want {
			t.Errorf("%q set route to %v, want %v", r, m.route, want)
		}
	}

	// q quits — but only when no screen is collecting text.
	_, cmd := base.Update(key('q'))
	if cmd == nil {
		t.Fatal("q produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("q produced %T, want tea.QuitMsg", cmd())
	}
}

// While a screen owns the keyboard, global shortcuts must not fire: quitting
// mid-answer or jumping screens mid-edit would lose the user's typing.
func TestTypingSuppressesGlobalKeys(t *testing.T) {
	m := NewModel("/tmp/ws", "")
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, key('2')) // new project form
	if !m.typing() {
		t.Fatal("form should own the keyboard")
	}
	_, cmd := m.Update(key('q'))
	if cmd != nil {
		if _, isQuit := cmd().(tea.QuitMsg); isQuit {
			t.Error("q quit while a form was being typed into")
		}
	}
	if m.route != routeNew {
		t.Errorf("route changed to %v while typing", m.route)
	}
}

func TestSidebarFilter(t *testing.T) {
	m := NewModel("/tmp/ws", "")
	m.projects = []app.Summary{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})

	if got := len(m.visibleProjects()); got != 3 {
		t.Fatalf("unfiltered = %d, want 3", got)
	}
	m.filter = "a"
	if got := len(m.visibleProjects()); got != 3 { // alpha, beta, gamma all contain "a"
		t.Errorf("filter 'a' = %d, want 3", got)
	}
	m.filter = "ga"
	got := m.visibleProjects()
	if len(got) != 1 || got[0].Name != "gamma" {
		t.Errorf("filter 'ga' = %+v, want gamma", got)
	}
	m.cursor = 5 // out of range after filtering must not panic the view
	_ = m.View()
	if c := m.current(); c != nil {
		t.Errorf("current() = %+v out of range, want nil", c)
	}
}

func TestPaletteOpensFiltersAndCloses(t *testing.T) {
	m := NewModel("/tmp/ws", "")
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = step(t, m, ctrlK())
	if m.pal == nil {
		t.Fatal("ctrl+k did not open the palette")
	}
	if len(m.pal.matches()) == 0 {
		t.Error("palette has no actions")
	}
	m = step(t, m, key('o'))
	if m.pal.filter != "o" {
		t.Errorf("filter = %q, want o", m.pal.filter)
	}
	m = step(t, m, esc())
	if m.pal != nil {
		t.Error("esc did not close the palette")
	}
}

// The frame must never be taller than the terminal: one row too many and the
// terminal scrolls the top bar out of view. This is a regression test for an
// off-by-one where the body was sized for three lines of chrome while the top
// bar and status bar are both bordered — four.
func TestViewFitsTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{{60, 6}, {80, 8}, {80, 20}, {100, 24}, {130, 40}, {200, 60}}
	routes := []route{routeHome, routeNew, routeProject, routeOrg, routeDoctor}

	for _, size := range sizes {
		for _, r := range routes {
			for _, help := range []bool{false, true} {
				m := NewModel("/tmp/ws", "")
				m.projects = []app.Summary{{Name: "alpha", Phase: "building", Running: true, Total: 4, Done: 1}}
				m.width, m.height, m.route, m.showHelp = size.w, size.h, r, help

				got := lipgloss.Height(m.View())
				if got > size.h {
					t.Errorf("%dx%d route=%v help=%v: view is %d lines, terminal is %d — it would scroll",
						size.w, size.h, r, help, got, size.h)
				}
			}
		}
	}
}

// The palette owns a whole canvas while open, so it must match the terminal
// exactly rather than being appended to the frame.
func TestPaletteViewIsExactlyTerminalHeight(t *testing.T) {
	for _, h := range []int{10, 24, 40} {
		m := NewModel("/tmp/ws", "")
		m.width, m.height = 120, h
		m.pal = newPalette(m.actions())
		if got := lipgloss.Height(m.View()); got != h {
			t.Errorf("palette at height %d rendered %d lines", h, got)
		}
	}
}

func TestEscStepsBackOneLevelThenHome(t *testing.T) {
	m := NewModel("/tmp/ws", "")
	m = step(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m.route = routeProject
	m.proj.tab = tabFiles // deeper than the overview
	m = step(t, m, esc())
	if m.route != routeProject {
		t.Errorf("first esc left the project (route=%v); it should return to Overview first", m.route)
	}
	if m.proj.tab != tabOverview {
		t.Errorf("tab = %d, want Overview", m.proj.tab)
	}
	m = step(t, m, esc())
	if m.route != routeHome {
		t.Errorf("second esc route = %v, want home", m.route)
	}
}
