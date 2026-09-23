package web

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dylan-demolder/factory/internal/state"
)

// blockedProject creates a project and rewrites its state the way a finished
// run with two blocked tasks leaves it: phase done, one blocked task with a
// done dependency (retryable), one done task.
func blockedProject(t *testing.T, e *env, name string) string {
	t.Helper()
	if code, out := e.do("POST", "/api/projects", map[string]any{"name": name, "idea": "a project with blocked work"}); code != 201 && code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	dir := filepath.Join(e.ws, name)
	store := &state.Store{Root: dir}
	p, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	p.Phase = state.PhaseDone
	p.Outcome = "finished-with-issues"
	p.Tasks = []*state.Task{
		{ID: "T1", Title: "shipped", Status: state.TaskDone, Attempts: 1, Commit: "abc"},
		{ID: "T4", Title: "file error path", Status: state.TaskBlocked, Attempts: 4,
			DependsOn: []string{"T1"},
			Notes:     []string{"gave up after 4 attempts"}},
		{ID: "T5", Title: "follows T4", Status: state.TaskBlocked, Attempts: 0,
			DependsOn: []string{"T4"},
			Notes:     []string{"blocked because dependency T4 is blocked"}},
	}
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTaskRetryEndpoint(t *testing.T) {
	e := setup(t, nil)
	e.login()
	blockedProject(t, e, "recover")

	code, out := e.do("POST", "/api/projects/recover/tasks/T4/retry", nil)
	if code != 200 {
		t.Fatalf("retry: %d %v", code, out)
	}
	if out["task"] != "T4" {
		t.Errorf("task = %v", out["task"])
	}
	// The dependent was blocked by T4, so it must come back with it.
	released := out["released"].([]any)
	if len(released) != 1 || released[0] != "T5" {
		t.Errorf("released = %v, want [T5]", released)
	}
	// A finished project must reopen, or starting a build would do nothing.
	sum := out["summary"].(map[string]any)
	if sum["phase"] != state.PhaseBuilding {
		t.Errorf("phase = %v, want building", sum["phase"])
	}
	if sum["blocked"] != float64(0) {
		t.Errorf("blocked count = %v, want 0", sum["blocked"])
	}

	// Retrying the same task twice is a conflict, not a silent no-op.
	if code, _ := e.do("POST", "/api/projects/recover/tasks/T4/retry", nil); code != 409 {
		t.Errorf("second retry = %d, want 409", code)
	}
	// A done task cannot be retried.
	if code, _ := e.do("POST", "/api/projects/recover/tasks/T1/retry", nil); code != 409 {
		t.Errorf("done task = %d, want 409", code)
	}
	// An unknown task is 404, not 500.
	if code, _ := e.do("POST", "/api/projects/recover/tasks/T404/retry", nil); code != 404 {
		t.Errorf("unknown task = %d, want 404", code)
	}
}

// The engine keeps this state in memory, so a live build would overwrite the
// edit on its next save.
func TestTaskRetryRefusesWhileRunning(t *testing.T) {
	e := setup(t, nil)
	e.login()
	dir := blockedProject(t, e, "busy2")

	if err := os.WriteFile(filepath.Join(dir, ".factory", "run.pid"),
		[]byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := e.do("POST", "/api/projects/busy2/tasks/T4/retry", nil)
	if code != 409 {
		t.Fatalf("retry while running = %d %v, want 409", code, out)
	}
	if msg, _ := out["error"].(string); msg == "" {
		t.Error("no explanation for the conflict")
	}
}

func TestTaskRetryRequiresAuth(t *testing.T) {
	e := setup(t, nil)
	if code, _ := e.do("POST", "/api/projects/x/tasks/T1/retry", nil); code != 401 {
		t.Errorf("status = %d, want 401", code)
	}
}
