// Package state persists a project's progress under <project>/.factory so a
// run can be stopped and resumed at any point.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Project phases, in order.
const (
	PhaseSpec      = "spec"      // interviewing / drafting; needs the human
	PhaseSpecified = "specified" // spec approved; ready to plan
	PhaseBuilding  = "building"
	PhaseAccepting = "accepting"
	PhaseDone      = "done"
)

// Task statuses.
const (
	TaskPending = "pending"
	TaskActive  = "in_progress"
	TaskDone    = "done"
	TaskBlocked = "blocked"
)

type QA struct {
	Q string `json:"q"`
	A string `json:"a"`
}

type Feature struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Acceptance  []string `json:"acceptance"`
}

type UseCase struct {
	ID       string   `json:"id"`
	Actor    string   `json:"actor"`
	Goal     string   `json:"goal"`
	Scenario string   `json:"scenario"`
	Success  string   `json:"success"`
	Verdict  string   `json:"verdict,omitempty"` // "", satisfied, unsatisfied
	Gaps     []string `json:"gaps,omitempty"`
}

// SpecData is the structured part of the spec (SPEC.md holds the prose).
type SpecData struct {
	Summary       string    `json:"summary"`
	Features      []Feature `json:"features"`
	UseCases      []UseCase `json:"use_cases"`
	NonGoals      []string  `json:"non_goals"`
	Stack         string    `json:"stack"`
	TestCommand   string    `json:"test_command"`
	OpenQuestions []string  `json:"open_questions,omitempty"`
}

type Task struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Kind        string   `json:"kind"`
	Description string   `json:"description"`
	Acceptance  []string `json:"acceptance"`
	DependsOn   []string `json:"depends_on"`
	UseCases    []string `json:"use_cases"`

	Status       string   `json:"status"`
	Attempts     int      `json:"attempts"`
	Brief        string   `json:"brief,omitempty"`
	LastFeedback string   `json:"last_feedback,omitempty"`
	Notes        []string `json:"notes,omitempty"`
	Commit       string   `json:"commit,omitempty"`
}

type Project struct {
	Name            string    `json:"name"`
	Idea            string    `json:"idea"`
	Phase           string    `json:"phase"`
	Interview       []QA      `json:"interview"`
	Spec            SpecData  `json:"spec"`
	Tasks           []*Task   `json:"tasks"`
	TestCommand     string    `json:"test_command"`
	AcceptanceRound int       `json:"acceptance_round"`
	Outcome         string    `json:"outcome,omitempty"`
	Created         time.Time `json:"created"`
	Updated         time.Time `json:"updated"`
}

// Task returns the task with id, or nil.
func (p *Project) Task(id string) *Task {
	for _, t := range p.Tasks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Counts returns done, blocked and total task counts.
func (p *Project) Counts() (done, blocked, total int) {
	for _, t := range p.Tasks {
		switch t.Status {
		case TaskDone:
			done++
		case TaskBlocked:
			blocked++
		}
	}
	return done, blocked, len(p.Tasks)
}

// Store reads and writes a project's .factory directory.
type Store struct {
	Root string // project repository root
}

func (s *Store) Dir() string { return filepath.Join(s.Root, ".factory") }
func (s *Store) Path(parts ...string) string {
	return filepath.Join(append([]string{s.Dir()}, parts...)...)
}
func (s *Store) Exists() bool {
	_, err := os.Stat(s.Path("state.json"))
	return err == nil
}

func (s *Store) Load() (*Project, error) {
	data, err := os.ReadFile(s.Path("state.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s is not a factory project (no .factory/state.json)", s.Root)
		}
		return nil, err
	}
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("corrupt state.json: %w", err)
	}
	return &p, nil
}

// Save writes state atomically.
func (s *Store) Save(p *Project) error {
	p.Updated = time.Now().UTC()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return s.Write("state.json", string(data))
}

// Write atomically writes a file under .factory.
func (s *Store) Write(rel, content string) error {
	path := s.Path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Event appends a line to .factory/events.jsonl.
func (s *Store) Event(kind, msg string) {
	f, err := os.OpenFile(s.Path("events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(map[string]string{"time": time.Now().UTC().Format(time.RFC3339), "kind": kind, "msg": msg})
	f.Write(append(line, '\n'))
}

// Pid returns the pid recorded for a running `factory run`, or 0.
func (s *Store) Pid() int {
	data, err := os.ReadFile(s.Path("run.pid"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid
}

func (s *Store) WritePid(pid int) error { return s.Write("run.pid", strconv.Itoa(pid)) }
func (s *Store) ClearPid()              { os.Remove(s.Path("run.pid")) }
