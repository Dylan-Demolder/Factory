package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dylan-demolder/factory/internal/state"
)

// Sentinel errors for task operations, so the HTTP layer can map them to
// status codes with errors.Is instead of matching on message text.
var (
	// ErrBuildRunning: the engine holds this state in memory and would
	// overwrite any edit on its next save.
	ErrBuildRunning = errors.New("a build is running — stop it before changing tasks")
	// ErrNoSuchTask is wrapped as `no such task "T4"`.
	ErrNoSuchTask = errors.New("no such task")
	// ErrAlreadyDone is wrapped as `T4 is already done`.
	ErrAlreadyDone = errors.New("is already done")
	// ErrNotBlocked is wrapped as `T4 is not blocked (status …)`.
	ErrNotBlocked = errors.New("is not blocked")
)

// RetryTask puts a blocked task back in the queue so a human can recover
// from it — factory's own answer to "gave up after N attempts" was to save a
// patch and stop, with no way back in through any interface.
//
// Three things have to move together or the retry does nothing useful:
//
//   - the task itself: status back to pending and its attempts reset, or the
//     very next pass would immediately exceed max_task_attempts again;
//   - its dependents: nextTask() wrote "blocked" onto tasks that depend on it,
//     and nothing re-evaluates that until the dependency is pending again;
//   - the phase: a project that already finished would send `factory run`
//     straight to Finish(), never looking at the task at all.
//
// It refuses while a build is running, because the engine holds this state in
// memory and would overwrite the edit on its next save.
func (pr *Project) RetryTask(id string) (released []string, err error) {
	if _, running := pr.Running(); running {
		return nil, ErrBuildRunning
	}
	t := pr.P.Task(id)
	if t == nil {
		return nil, fmt.Errorf("%w %q", ErrNoSuchTask, id)
	}
	if t.Status == state.TaskDone {
		return nil, fmt.Errorf("%s %w", id, ErrAlreadyDone)
	}
	if t.Status != state.TaskBlocked {
		return nil, fmt.Errorf("%s %w (status %q) — retry applies to blocked tasks", id, ErrNotBlocked, t.Status)
	}

	t.Status = state.TaskPending
	t.Attempts = 0
	t.Notes = append(t.Notes, "retried by hand; attempts reset")

	for _, other := range pr.P.Tasks {
		if other.ID == id || other.Status != state.TaskBlocked {
			continue
		}
		if !causedBy(other, id) {
			continue
		}
		other.Status = state.TaskPending
		other.Attempts = 0
		other.Notes = append(other.Notes, "unblocked: dependency "+id+" was retried")
		released = append(released, other.ID)
	}

	if pr.P.Phase == state.PhaseDone || pr.P.Phase == state.PhaseAccepting {
		pr.P.Phase = state.PhaseBuilding
		pr.P.Outcome = ""
	}
	if err := pr.Store.Save(pr.P); err != nil {
		return nil, fmt.Errorf("could not save the project: %w", err)
	}
	return released, nil
}

// causedBy reports whether other was blocked by dependency id — either
// through its declared depends_on or through the note nextTask() writes when
// it propagates a block, which can name a task that is not in the list.
func causedBy(other *state.Task, id string) bool {
	for _, d := range other.DependsOn {
		if d == id {
			return true
		}
	}
	for _, n := range other.Notes {
		if strings.Contains(n, "dependency "+id+" ") || strings.HasSuffix(n, "dependency "+id) {
			return true
		}
	}
	return false
}

// BlockedTasks lists blocked tasks with their last failure, so a caller can
// show a human what actually went wrong before offering a retry.
func (pr *Project) BlockedTasks() []*state.Task {
	var out []*state.Task
	for _, t := range pr.P.Tasks {
		if t.Status == state.TaskBlocked {
			out = append(out, t)
		}
	}
	return out
}
