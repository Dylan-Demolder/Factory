package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dylan-demolder/factory/internal/state"
)

// projectFixture writes a project whose phases and tasks mirror wordcnt's
// real ending: finished-with-issues, one blocked task, one dependent that
// nextTask() blocked along with it, and one blocked for unrelated reasons.
func projectFixture(t *testing.T) *Project {
	t.Helper()
	dir := t.TempDir()
	store := &state.Store{Root: dir}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &state.Project{
		Name:            "demo",
		Phase:           state.PhaseDone,
		Outcome:         "finished-with-issues",
		AcceptanceRound: 2,
		Tasks: []*state.Task{
			{ID: "T4", Title: "file error path", Status: state.TaskBlocked, Attempts: 4,
				Notes: []string{"gave up after 4 attempts; last feedback: …"}},
			{ID: "T5", Title: "depends on T4", Status: state.TaskBlocked, Attempts: 0,
				DependsOn: []string{"T4"},
				Notes:     []string{"blocked because dependency T4 is blocked"}},
			{ID: "T6", Title: "blocked elsewhere", Status: state.TaskBlocked, Attempts: 0,
				Notes: []string{"blocked because dependency T9 is blocked"}},
			{ID: "T1", Title: "already shipped", Status: state.TaskDone, Attempts: 1, Commit: "abc123"},
		},
	}
	if err := store.Save(p); err != nil {
		t.Fatal(err)
	}
	return &Project{Store: store, P: p}
}

func TestRetryBlockedTaskReopensTheProject(t *testing.T) {
	pr := projectFixture(t)

	released, err := pr.RetryTask("T4")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	t4 := pr.P.Task("T4")
	if t4.Status != state.TaskPending {
		t.Errorf("T4 status = %q, want pending", t4.Status)
	}
	if t4.Attempts != 0 {
		t.Errorf("T4 attempts = %d, want 0 — otherwise the next pass is over the limit immediately", t4.Attempts)
	}
	if len(t4.Notes) == 0 || !strings.Contains(t4.Notes[len(t4.Notes)-1], "retried by hand") {
		t.Errorf("T4 notes not annotated: %v", t4.Notes)
	}

	// The dependent must come back too, or nothing could ever run.
	if len(released) != 1 || released[0] != "T5" {
		t.Errorf("released = %v, want [T5]", released)
	}
	if s := pr.P.Task("T5").Status; s != state.TaskPending {
		t.Errorf("T5 status = %q, want pending", s)
	}
	// A blocked task with an unrelated cause must be left alone.
	if s := pr.P.Task("T6").Status; s != state.TaskBlocked {
		t.Errorf("T6 was released although it does not depend on T4: %q", s)
	}
	if pr.P.Task("T1").Status != state.TaskDone || pr.P.Task("T1").Commit != "abc123" {
		t.Error("a finished task was disturbed")
	}

	// A finished project would send `factory run` to Finish() and never
	// revisit these tasks: the phase has to reopen.
	if pr.P.Phase != state.PhaseBuilding {
		t.Errorf("phase = %q, want building", pr.P.Phase)
	}
	if pr.P.Outcome != "" {
		t.Errorf("outcome = %q, want cleared", pr.P.Outcome)
	}
	if pr.P.AcceptanceRound != 2 {
		t.Errorf("acceptance round = %d — it should be kept, not replayed", pr.P.AcceptanceRound)
	}

	// And it must be on disk, not only in memory.
	reloaded, err := (&state.Store{Root: pr.Store.Root}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Task("T4").Status != state.TaskPending || reloaded.Phase != state.PhaseBuilding {
		t.Errorf("saved state: T4=%q phase=%q", reloaded.Task("T4").Status, reloaded.Phase)
	}
}

func TestRetryRefusesWhatItCannotHelp(t *testing.T) {
	pr := projectFixture(t)
	if _, err := pr.RetryTask("T404"); err == nil || !strings.Contains(err.Error(), "no such task") {
		t.Errorf("unknown task: %v", err)
	}
	if _, err := pr.RetryTask("T1"); err == nil || !strings.Contains(err.Error(), "already done") {
		t.Errorf("done task: %v", err)
	}

	// Move T6 out of blocked to prove the status check.
	pr.P.Task("T6").Status = state.TaskActive
	if _, err := pr.RetryTask("T6"); err == nil || !strings.Contains(err.Error(), "not blocked") {
		t.Errorf("non-blocked task: %v", err)
	}
}

// A running build holds this state in memory and would overwrite the edit on
// its next save, so the change must be refused while one is alive.
func TestRetryRefusesWhileABuildRuns(t *testing.T) {
	pr := projectFixture(t)
	if err := os.WriteFile(filepath.Join(pr.Store.Dir(), "run.pid"),
		[]byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pr.RetryTask("T4"); err == nil || !strings.Contains(err.Error(), "stop it") {
		t.Fatalf("err = %v, want a refusal naming the running build", err)
	}
	// Nothing may have changed.
	if s := pr.P.Task("T4").Status; s != state.TaskBlocked {
		t.Errorf("state changed despite the refusal: %q", s)
	}
}
