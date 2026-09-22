// Package app holds project operations shared by the CLI and the web
// interface: create, open, start/stop background runs.
package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/pipeline"
	"github.com/dylan-demolder/factory/internal/proc"
	"github.com/dylan-demolder/factory/internal/state"
)

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether name is usable as a project (directory) name.
func ValidName(name string) error {
	if !nameRe.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid project name %q: use letters, digits, '.', '_' or '-' (max 64)", name)
	}
	return nil
}

// Delete removes a project directory.
//
// Two refusals make this safe enough to expose to an API: a build that is
// still running must be stopped first (deleting out from under a live
// `factory run` would leave it writing into a hole), and the path must be a
// factory project sitting inside a parent directory — so no crafted name can
// turn a delete into a walk outside the workspace.
func Delete(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	parent := filepath.Dir(abs)
	if abs == string(filepath.Separator) || parent == abs || parent == string(filepath.Separator) {
		return fmt.Errorf("refusing to delete %s: not inside a workspace", abs)
	}
	store := &state.Store{Root: abs}
	if !store.Exists() {
		return fmt.Errorf("%s is not a factory project", abs)
	}
	if pid := store.Pid(); proc.Alive(pid) {
		return fmt.Errorf("a build is running (pid %d) — stop it first", pid)
	}
	if err := os.RemoveAll(abs); err != nil {
		return fmt.Errorf("could not delete %s: %w", abs, err)
	}
	return nil
}

// Project is an opened factory project.
type Project struct {
	Cfg     *config.Config
	CfgPath string
	Store   *state.Store
	P       *state.Project
}

// Open loads the project in dir. cfgFlag overrides config resolution.
func Open(dir, cfgFlag string) (*Project, error) {
	store := &state.Store{Root: dir}
	p, err := store.Load()
	if err != nil {
		return nil, err
	}
	cfg, path, err := config.Resolve(cfgFlag, dir)
	if err != nil {
		return nil, err
	}
	return &Project{Cfg: cfg, CfgPath: path, Store: store, P: p}, nil
}

// Create makes a new project in dir and snapshots the config into it so
// detached and resumed runs use the same setup.
func Create(dir, name, idea string, cfg *config.Config, cfgPath string) (*Project, error) {
	store := &state.Store{Root: dir}
	if store.Exists() {
		return nil, fmt.Errorf("%s is already a factory project", dir)
	}
	if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	if err := store.Write("config.json", strings.ReplaceAll(string(raw), "{{config_dir}}", cfg.Dir)); err != nil {
		return nil, err
	}
	p := &state.Project{Name: name, Idea: strings.TrimSpace(idea), Phase: state.PhaseSpec, Created: time.Now().UTC()}
	if err := store.Save(p); err != nil {
		return nil, err
	}
	return &Project{Cfg: cfg, CfgPath: store.Path("config.json"), Store: store, P: p}, nil
}

// Engine builds a pipeline engine for the project.
func (pr *Project) Engine(out io.Writer) (*pipeline.Engine, error) {
	agents, err := agent.NewAll(pr.Cfg)
	if err != nil {
		return nil, err
	}
	return pipeline.New(pr.Cfg, agents, pr.Store, pr.P, out), nil
}

// Running returns the pid of an active background run.
func (pr *Project) Running() (int, bool) {
	pid := pr.Store.Pid()
	return pid, proc.Alive(pid)
}

// ResetForSpec prepares a project that is past the spec phase for a new
// spec interview. Code and git history are kept.
func (pr *Project) ResetForSpec() error {
	if pid, ok := pr.Running(); ok {
		return fmt.Errorf("a build is running (pid %d); stop it first", pid)
	}
	p := pr.P
	p.Phase = state.PhaseSpec
	p.Tasks = nil
	p.AcceptanceRound = 0
	p.Outcome = ""
	for i := range p.Spec.UseCases {
		p.Spec.UseCases[i].Verdict, p.Spec.UseCases[i].Gaps = "", nil
	}
	return pr.Store.Save(p)
}

// StartRun launches `factory run` for the project as a detached background
// process, logging to .factory/run.log. exe is the factory binary ("" = this one).
func (pr *Project) StartRun(exe string) (int, error) {
	if pid, ok := pr.Running(); ok {
		return pid, fmt.Errorf("already running (pid %d)", pid)
	}
	if pr.P.Phase == state.PhaseSpec {
		return 0, errors.New("the spec has not been approved yet")
	}
	if exe == "" {
		var err error
		if exe, err = os.Executable(); err != nil {
			return 0, err
		}
	}
	logf, err := os.OpenFile(pr.Store.Path("run.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, err
	}
	defer logf.Close()
	sep := ""
	if fi, err := logf.Stat(); err == nil && fi.Size() > 0 {
		sep = "\n"
	}
	fmt.Fprintf(logf, "%s===== run started %s =====\n", sep, time.Now().Format(time.RFC1123))
	cmd := exec.Command(exe, "run", pr.Store.Root, "--config", pr.CfgPath)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Dir = pr.Store.Root
	proc.Detach(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := pr.Store.WritePid(pid); err != nil {
		return pid, err
	}
	// Reap the child if this process outlives it (e.g. the web server).
	go cmd.Wait()
	return pid, nil
}

// Stop asks a background run to stop; progress is saved and it can resume.
func (pr *Project) Stop() error {
	pid, ok := pr.Running()
	if !ok {
		pr.Store.ClearPid()
		return errors.New("no build is running")
	}
	return proc.Terminate(pid)
}

// StopAndWait terminates a running build and waits for it to actually exit.
//
// Terminate only sends the signal, so a caller that stops a build and then
// deletes the project would race the process it just signalled — Delete's own
// running check would (rightly) still see it alive and refuse. Waiting here
// keeps that guard strict and gives every caller the same behaviour.
func (pr *Project) StopAndWait(timeout time.Duration) error {
	if _, ok := pr.Running(); !ok {
		pr.Store.ClearPid()
		return nil
	}
	if err := pr.Stop(); err != nil {
		return fmt.Errorf("could not stop the build: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := pr.Running(); !ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, ok := pr.Running(); ok {
		return errors.New("the build did not stop")
	}
	return nil
}

// Summary is a compact view of a project for listings.
type Summary struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Dir       string    `json:"dir"`
	Phase     string    `json:"phase"`
	Outcome   string    `json:"outcome,omitempty"`
	Running   bool      `json:"running"`
	Pid       int       `json:"pid,omitempty"`
	Done      int       `json:"done"`
	Blocked   int       `json:"blocked"`
	Total     int       `json:"total"`
	UseCases  int       `json:"use_cases"`
	Satisfied int       `json:"satisfied"`
	Updated   time.Time `json:"updated"`
}

// Summarize loads a project's state for listing without resolving config.
func Summarize(id, dir string) (Summary, error) {
	store := &state.Store{Root: dir}
	p, err := store.Load()
	if err != nil {
		return Summary{}, err
	}
	pid := store.Pid()
	running := proc.Alive(pid)
	if !running {
		pid = 0
	}
	done, blocked, total := p.Counts()
	s := Summary{ID: id, Name: p.Name, Dir: dir, Phase: p.Phase, Outcome: p.Outcome, Running: running, Pid: pid,
		Done: done, Blocked: blocked, Total: total, UseCases: len(p.Spec.UseCases), Updated: p.Updated}
	for _, u := range p.Spec.UseCases {
		if u.Verdict == "satisfied" {
			s.Satisfied++
		}
	}
	return s, nil
}

// List returns every project directly under workspace, newest first.
func List(workspace string) ([]Summary, error) {
	entries, err := os.ReadDir(workspace)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if !e.IsDir() || ValidName(e.Name()) != nil {
			continue
		}
		s, err := Summarize(e.Name(), filepath.Join(workspace, e.Name()))
		if err != nil {
			continue
		}
		out = append(out, s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Updated.After(out[j-1].Updated); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}
