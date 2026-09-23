package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/state"
)

// tasksModel builds a Model showing a Tasks tab over a real on-disk project,
// so the retry path exercises the same operation the CLI and API use.
func tasksModel(t *testing.T, tasks ...*state.Task) Model {
	t.Helper()
	dir := t.TempDir()
	store := &state.Store{Root: dir}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &state.Project{Name: "demo", Phase: state.PhaseDone, Outcome: "finished-with-issues", Tasks: tasks}
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	m := NewModel(filepath.Dir(dir), "")
	m.route = routeProject
	m.proj = projectState{
		pr:  &app.Project{Store: store, P: p},
		st:  p,
		tab: tabTasks,
	}
	return m
}

func TestFirstBlockedTaskSelectsTheProblem(t *testing.T) {
	tasks := []*state.Task{
		{ID: "T1", Status: state.TaskDone},
		{ID: "T4", Status: state.TaskBlocked},
		{ID: "T8", Status: state.TaskBlocked},
	}
	if got := firstBlockedTask(tasks); got != 1 {
		t.Errorf("first blocked = %d, want 1 (T4) — the tab should land on the problem", got)
	}
	if got := firstBlockedTask([]*state.Task{{ID: "T1", Status: state.TaskDone}}); got != 0 {
		t.Errorf("all-done list = %d, want 0", got)
	}
}

// Re-queueing takes two presses of u: one warns, the second acts. One press
// must never change state.
func TestTaskRetryNeedsTwoPresses(t *testing.T) {
	m := tasksModel(t,
		&state.Task{ID: "T4", Status: state.TaskBlocked, Attempts: 4,
			Notes: []string{"gave up after 4 attempts"}},
		&state.Task{ID: "T1", Status: state.TaskDone},
	)
	m.proj.taskSel = 0 // T4

	// First press: a warning, no state change.
	next, cmd := m.tasksKey(tea.KeyMsg{}, "u")
	model := next.(Model)
	if model.proj.taskConfirm != "T4" {
		t.Fatalf("confirm = %q, want T4", model.proj.taskConfirm)
	}
	if cmd == nil {
		t.Fatal("first press produced no warning")
	}
	if msg := cmd(); msg != nil {
		if _, ok := msg.(statusMsg); !ok {
			t.Errorf("first press message = %T, want statusMsg", msg)
		}
	}
	if model.proj.st.Tasks[0].Status != state.TaskBlocked {
		t.Fatal("one press changed the state — it should only warn")
	}

	// Second press: the real operation. Note it must go to the model returned
	// by the first press — tasksKey has a value receiver, so the armed
	// confirmation lives there, not in the original.
	next, cmd = model.tasksKey(tea.KeyMsg{}, "u")
	model = next.(Model)
	if model.proj.taskConfirm != "" {
		t.Errorf("confirm not cleared: %q", model.proj.taskConfirm)
	}
	if cmd == nil {
		t.Fatal("second press produced no command")
	}
	msg, ok := cmd().(taskRetryMsg)
	if !ok {
		t.Fatalf("message = %T, want taskRetryMsg", cmd())
	}
	if msg.err != nil {
		t.Fatalf("retry: %v", msg.err)
	}
	if msg.id != "T4" {
		t.Errorf("id = %q", msg.id)
	}
	if got := model.proj.st.Tasks[0].Status; got != state.TaskPending {
		t.Errorf("T4 status = %q, want pending", got)
	}
}

// A task that is not blocked must be told so rather than silently "retried".
func TestTaskRetryRefusesANonBlockedTask(t *testing.T) {
	m := tasksModel(t, &state.Task{ID: "T1", Status: state.TaskDone})
	m.proj.taskSel = 0

	_, cmd := m.tasksKey(tea.KeyMsg{}, "u")
	if cmd == nil {
		t.Fatal("no feedback for retrying a done task")
	}
	msg, ok := cmd().(statusMsg)
	if !ok {
		t.Fatalf("message = %T, want statusMsg", cmd())
	}
	if !msg.bad || !strings.Contains(msg.text, "re-queue applies to blocked") {
		t.Errorf("status = %+v, want a refusal explaining re-queue only applies to blocked tasks", msg)
	}
	if m.proj.taskConfirm != "" {
		t.Error("a refused retry armed the confirmation")
	}
}

// The Tasks tab must render without panicking, including for an empty project
// and for the selected-task detail.
func TestViewTasksHandlesEmptyAndSelection(t *testing.T) {
	empty := tasksModel(t) // no tasks at all
	if got := empty.viewTasks(80, 20); !strings.Contains(got, "No tasks yet") {
		t.Errorf("empty view = %q", got)
	}

	m := tasksModel(t,
		&state.Task{ID: "T1", Status: state.TaskDone, Title: "done task"},
		&state.Task{ID: "T4", Status: state.TaskBlocked, Attempts: 4, Title: "blocked task",
			Notes: []string{"gave up after 4 attempts"}},
	)
	m.proj.taskSel = 99 // out of range: must clamp, not panic
	out := m.viewTasks(60, 24)
	for _, want := range []string{"1/2 done", "1 blocked", "T4", "press u twice"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}
