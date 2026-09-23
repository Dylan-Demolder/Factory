package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/state"
	"github.com/dylan-demolder/factory/internal/ui"
)

// fake is a scripted agent. handler receives the request and the number of
// times that stage has been seen before.
type fake struct {
	name    string
	mu      sync.Mutex
	counts  map[string]int
	calls   []agent.Request
	handler func(req agent.Request, n int) (string, error)
}

func (f *fake) Name() string { return f.name }
func (f *fake) Run(_ context.Context, req agent.Request) (string, error) {
	f.mu.Lock()
	if f.counts == nil {
		f.counts = map[string]int{}
	}
	n := f.counts[req.Stage]
	f.counts[req.Stage]++
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	return f.handler(req, n)
}

func (f *fake) stages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var s []string
	for _, c := range f.calls {
		s = append(s, c.Stage)
	}
	return s
}

func testConfig(t *testing.T, mutate func(c *config.Config)) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte(`{
		"agents": {"builder":{"type":"opencode"},"thinker":{"type":"command","command":"true"},"critic":{"type":"command","command":"true"}},
		"roles": {"interviewer":"thinker","planner":"thinker","builder":"builder","reviewer":"critic","moderator":"thinker"},
		"roundtable": {"rounds": 2, "participants": [
			{"agent":"builder","persona":"engineer"},
			{"agent":"thinker","persona":"user advocate"},
			{"agent":"critic","persona":"QA"}
		]},
		"limits": {"agent_retries": 1}
	}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(c)
	}
	return c
}

const specReply = "# Greeter\n\n## Summary\nSays hello.\n\n```json\n" + `{"summary":"CLI that greets people","features":[{"id":"F1","title":"Greet","description":"greet by name","acceptance":["prints Hello, NAME"]}],"use_cases":[{"id":"UC1","actor":"new user","goal":"get greeted","scenario":"run ./hello.sh Ann","success":"sees Hello, Ann"}],"non_goals":[],"stack":"POSIX sh","test_command":"sh test.sh","open_questions":[]}` + "\n```"

const planReplyJSON = "```json\n" + `{"test_command":"sh test.sh","tasks":[
 {"id":"T1","title":"Skeleton and test runner","kind":"setup","description":"test.sh runs tests/*.sh","acceptance":["sh test.sh runs"],"depends_on":[]},
 {"id":"T2","title":"Greeting","kind":"feature","description":"hello.sh NAME","acceptance":["prints Hello, NAME"],"depends_on":["T1"]},
 {"id":"T3","title":"UC1 end-to-end test","kind":"usecase","description":"e2e","acceptance":["e2e passes"],"depends_on":["T2"],"use_cases":["UC1"]}
]}` + "\n```"

var taskRe = regexp.MustCompile(`## Your task\n### (\S+):`)

func taskID(prompt string) string {
	m := taskRe.FindStringSubmatch(prompt)
	if m == nil {
		return ""
	}
	return m[1]
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newEngine(t *testing.T, cfg *config.Config, agents ...*fake) (*Engine, string) {
	t.Helper()
	dir := t.TempDir()
	store := &state.Store{Root: dir}
	p := &state.Project{Name: "greeter", Idea: "a CLI that greets people", Phase: state.PhaseSpec}
	os.MkdirAll(store.Dir(), 0o755)
	m := map[string]agent.Agent{}
	for _, a := range agents {
		m[a.name] = a
	}
	e := New(cfg, m, store, p, io.Discard)
	e.Backoff = 0
	return e, dir
}

func gitLog(t *testing.T, dir string) []string {
	out, err := exec.Command("git", "-C", dir, "log", "--format=%s").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

func gitClean(t *testing.T, dir string) {
	out, _ := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("working tree not clean:\n%s", out)
	}
}

func TestFullPipeline(t *testing.T) {
	cfg := testConfig(t, nil)
	var dir string

	thinker := &fake{name: "thinker"}
	thinker.handler = func(req agent.Request, n int) (string, error) {
		switch {
		case req.Stage == "interview" && n == 0:
			return `{"questions":["Who is it for?","Any platform constraints?"],"ready":false}`, nil
		case req.Stage == "interview":
			return `{"questions":[],"ready":true}`, nil
		case req.Stage == "spec-draft":
			return "no json here, oops", nil // exercises the repair path
		case req.Stage == "spec-draft-repair", req.Stage == "roundtable-spec-synthesis":
			return specReply, nil
		case req.Stage == "spec-revise":
			return strings.Replace(specReply, "CLI that greets people", "CLI that greets people politely", 1), nil
		case req.Stage == "plan", req.Stage == "roundtable-plan-synthesis":
			return planReplyJSON, nil
		case strings.HasPrefix(req.Stage, "roundtable-task-") && strings.HasSuffix(req.Stage, "-synthesis"):
			return "## Approach\nKeep it simple.\n## Required tests\n- greets Ann", nil
		case req.Stage == "roundtable-acceptance-1-synthesis":
			return `{"use_cases":[{"id":"UC1","satisfied":false,"gaps":["no --help"]}],"fix_tasks":[{"title":"Add --help","description":"print usage","acceptance":["./hello.sh --help prints Usage"]}]}`, nil
		case req.Stage == "roundtable-acceptance-2-synthesis":
			return `{"use_cases":[{"id":"UC1","satisfied":true,"gaps":[]}],"fix_tasks":[]}`, nil
		case strings.HasPrefix(req.Stage, "roundtable-"):
			return "thinker opinion", nil
		}
		return "", fmt.Errorf("thinker: unexpected stage %s", req.Stage)
	}

	rejectedT3 := false
	critic := &fake{name: "critic"}
	critic.handler = func(req agent.Request, n int) (string, error) {
		switch {
		case req.Stage == "review" && strings.Contains(req.Prompt, "### T3:") && !rejectedT3:
			rejectedT3 = true
			return `{"verdict":"fail","issues":["e2e test doesn't check output"],"summary":"weak test"}`, nil
		case req.Stage == "review":
			if !strings.Contains(req.Prompt, "diff --git") {
				return "", fmt.Errorf("review prompt has no diff")
			}
			return "Looks good.\n```json\n{\"verdict\":\"pass\",\"issues\":[],\"summary\":\"ok\"}\n```", nil
		case strings.HasPrefix(req.Stage, "roundtable-"):
			return "critic opinion", nil
		}
		return "", fmt.Errorf("critic: unexpected stage %s", req.Stage)
	}

	builderFailedOnce := false
	builder := &fake{name: "builder"}
	builder.handler = func(req agent.Request, n int) (string, error) {
		if strings.HasPrefix(req.Stage, "roundtable-") {
			if !req.ReadOnly {
				return "", fmt.Errorf("roundtable call must be read-only")
			}
			if n == 0 && req.Stage == "roundtable-spec" {
				return "", fmt.Errorf("transient failure") // exercises retry
			}
			return "engineer opinion", nil
		}
		switch req.Stage {
		case "build":
			if req.ReadOnly {
				return "", fmt.Errorf("build must not be read-only")
			}
			switch id := taskID(req.Prompt); id {
			case "T1":
				write(t, dir, "test.sh", "set -e\nfor f in tests/*.sh; do [ -e \"$f\" ] || continue; sh \"$f\"; done\n")
				write(t, dir, "README.md", "# greeter\n")
			case "T2":
				if !builderFailedOnce {
					builderFailedOnce = true
					write(t, dir, "hello.sh", "echo Helo, \"$1\"\n")
				} else {
					if !strings.Contains(req.Prompt, "previous attempt was rejected") {
						return "", fmt.Errorf("feedback missing from retry prompt")
					}
					write(t, dir, "hello.sh", "if [ \"$1\" = --help ]; then echo Usage; exit 0; fi\necho Hello, \"$1\"\n")
				}
				write(t, dir, "tests/unit.sh", "[ \"$(sh hello.sh Bob)\" = 'Hello, Bob' ]\n")
			case "T3":
				write(t, dir, "tests/e2e.sh", "sh hello.sh Ann | grep -q 'Hello, Ann'\n")
			case "A1.1":
				write(t, dir, "tests/help.sh", "sh hello.sh --help | grep -q Usage\n")
			default:
				return "", fmt.Errorf("unexpected task %q", id)
			}
			return "done", nil
		case "trial":
			write(t, dir, "scratch.txt", "trial left this behind")
			return "I ran it.\n```json\n{\"results\":[{\"id\":\"UC1\",\"worked\":true,\"what_i_did\":\"sh hello.sh Ann\",\"observations\":\"fine\"}]}\n```", nil
		}
		return "", fmt.Errorf("builder: unexpected stage %s", req.Stage)
	}

	e, d := newEngine(t, cfg, thinker, critic, builder)
	dir = d
	// Answers: the round is asked as one batch (one line each — Q1, Q2
	// blank = no preference), then the change request, then approve.
	e.UI = ui.New(strings.NewReader("Busy developers\n\nBe more polite\n\ny\n"), io.Discard)

	ctx := context.Background()
	if err := e.Spec(ctx); err != nil {
		t.Fatal(err)
	}
	p := e.P
	if p.Phase != state.PhaseSpecified {
		t.Fatalf("phase = %s", p.Phase)
	}
	if len(p.Interview) != 3 || p.Interview[1].A != noPreference {
		t.Fatalf("interview = %+v", p.Interview)
	}
	if p.Spec.Summary != "CLI that greets people politely" {
		t.Fatalf("revision not applied: %q", p.Spec.Summary)
	}
	if md, _ := os.ReadFile(filepath.Join(dir, "SPEC.md")); !strings.HasPrefix(string(md), "# Greeter") {
		t.Fatalf("SPEC.md = %q", md)
	}

	if err := e.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if p.Phase != state.PhaseDone || p.Outcome != "complete" {
		t.Fatalf("phase=%s outcome=%s", p.Phase, p.Outcome)
	}
	wantAttempts := map[string]int{"T1": 1, "T2": 2, "T3": 2, "A1.1": 1}
	for _, tk := range p.Tasks {
		if tk.Status != state.TaskDone {
			t.Errorf("%s status %s", tk.ID, tk.Status)
		}
		if tk.Attempts != wantAttempts[tk.ID] {
			t.Errorf("%s attempts %d, want %d", tk.ID, tk.Attempts, wantAttempts[tk.ID])
		}
		if tk.Brief == "" {
			t.Errorf("%s has no roundtable brief", tk.ID)
		}
	}
	if len(p.Tasks) != 4 {
		t.Fatalf("tasks = %d", len(p.Tasks))
	}
	if p.AcceptanceRound != 2 || p.Spec.UseCases[0].Verdict != "satisfied" {
		t.Fatalf("acceptance round %d verdict %q", p.AcceptanceRound, p.Spec.UseCases[0].Verdict)
	}

	log := gitLog(t, dir)
	want := []string{"factory: build report", "A1.1: Add --help", "T3: UC1 end-to-end test", "T2: Greeting", "T1: Skeleton and test runner", "factory: initial commit"}
	if strings.Join(log, "|") != strings.Join(want, "|") {
		t.Fatalf("git log:\n%s", strings.Join(log, "\n"))
	}
	gitClean(t, dir)
	if _, err := os.Stat(filepath.Join(dir, "scratch.txt")); err == nil {
		t.Fatal("trial scratch file should have been discarded")
	}
	if _, err := os.Stat(filepath.Join(dir, "opencode.json")); err != nil {
		t.Fatal("opencode.json not written for the opencode builder")
	}
	report, _ := os.ReadFile(filepath.Join(dir, "REPORT.md"))
	if !strings.Contains(string(report), "**complete**") || !strings.Contains(string(report), "| UC1 |") {
		t.Fatalf("report:\n%s", report)
	}
	transcripts, _ := os.ReadDir(e.Store.Path("roundtables"))
	// spec, plan, 4 tasks, 2 acceptance rounds
	if len(transcripts) != 8 {
		t.Fatalf("got %d transcripts", len(transcripts))
	}
	first, _ := os.ReadFile(filepath.Join(e.Store.Path("roundtables"), transcripts[0].Name()))
	if !strings.Contains(string(first), "## Round 2") || !strings.Contains(string(first), "engineer opinion") {
		t.Fatalf("transcript:\n%s", first)
	}
	// Round 2 prompts must include the other panelists' round 1 positions.
	for _, c := range critic.calls {
		if c.Stage == "roundtable-plan" && strings.Contains(c.Prompt, "The other panelists said") {
			if !strings.Contains(c.Prompt, "engineer opinion") || !strings.Contains(c.Prompt, "thinker opinion") {
				t.Fatal("round 2 prompt lacks other positions")
			}
		}
	}
}

func TestBlockedTaskIsDiscardedAndDependentsBlocked(t *testing.T) {
	cfg := testConfig(t, func(c *config.Config) {
		c.Limits.MaxTaskAttempts = 2
		c.Limits.MaxAcceptanceRounds = 1
		f := false
		c.Roundtable.OnTasks = &f
		c.Roundtable.OnPlan = &f
	})
	var dir string
	thinker := &fake{name: "thinker", handler: func(req agent.Request, n int) (string, error) {
		switch {
		case req.Stage == "plan":
			return planReplyJSON, nil
		case req.Stage == "roundtable-acceptance-1-synthesis":
			return `{"use_cases":[{"id":"UC1","satisfied":false,"gaps":["greeting missing"]}],"fix_tasks":[]}`, nil
		case strings.HasPrefix(req.Stage, "roundtable-"):
			return "opinion", nil
		}
		return "", fmt.Errorf("unexpected %s", req.Stage)
	}}
	critic := &fake{name: "critic", handler: func(req agent.Request, n int) (string, error) {
		if req.Stage == "review" {
			return `{"verdict":"pass","issues":[],"summary":"ok"}`, nil
		}
		return "opinion", nil
	}}
	builder := &fake{name: "builder", handler: func(req agent.Request, n int) (string, error) {
		switch req.Stage {
		case "build":
			switch taskID(req.Prompt) {
			case "T1":
				write(t, dir, "test.sh", "set -e\nfor f in tests/*.sh; do [ -e \"$f\" ] || continue; sh \"$f\"; done\n")
			case "T2":
				write(t, dir, "tests/broken.sh", "exit 1\n") // never passes
			default:
				return "", fmt.Errorf("T3 must not be built when T2 is blocked")
			}
			return "done", nil
		case "trial":
			return `{"results":[{"id":"UC1","worked":false}]}`, nil
		}
		return "opinion", nil
	}}
	e, d := newEngine(t, cfg, thinker, critic, builder)
	dir = d
	spec := state.SpecData{}
	if _, data, err := parseSpec(specReply); err != nil {
		t.Fatal(err)
	} else {
		spec = data
	}
	e.P.Spec = spec
	e.P.Phase = state.PhaseSpecified
	write(t, dir, "SPEC.md", "# Greeter\n")

	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := e.P
	status := map[string]string{}
	for _, tk := range p.Tasks {
		status[tk.ID] = tk.Status
	}
	if status["T1"] != state.TaskDone || status["T2"] != state.TaskBlocked || status["T3"] != state.TaskBlocked {
		t.Fatalf("statuses: %v", status)
	}
	if p.Outcome != "finished-with-issues" {
		t.Fatalf("outcome %q", p.Outcome)
	}
	// The last acceptance round records gaps instead of queueing fixes.
	if len(p.Tasks) != 3 || p.Spec.UseCases[0].Verdict != "unsatisfied" || p.Spec.UseCases[0].Gaps[0] != "greeting missing" {
		t.Fatalf("tasks=%d usecase=%+v", len(p.Tasks), p.Spec.UseCases[0])
	}
	if _, err := os.Stat(filepath.Join(dir, "tests", "broken.sh")); err == nil {
		t.Fatal("blocked task's changes should be reverted")
	}
	patch, err := os.ReadFile(e.Store.Path("tasks", "T2-unfinished.patch"))
	if err != nil || !strings.Contains(string(patch), "tests/broken.sh") {
		t.Fatalf("patch not saved: %v", err)
	}
	gitClean(t, dir)
	for _, s := range critic.stages() {
		if s == "review-repair" {
			t.Fatal("reviewer output should have parsed first time")
		}
	}
}

func TestResumeResetsInProgressTask(t *testing.T) {
	cfg := testConfig(t, func(c *config.Config) {
		f := false
		c.Roundtable.OnTasks = &f
		c.Roundtable.Participants = nil
	})
	var dir string
	builds := 0
	builder := &fake{name: "builder", handler: func(req agent.Request, n int) (string, error) {
		if req.Stage == "build" {
			builds++
			write(t, dir, "test.sh", "true\n")
			return "done", nil
		}
		return `{"results":[]}`, nil
	}}
	thinker := &fake{name: "thinker", handler: func(req agent.Request, n int) (string, error) {
		return `{"use_cases":[{"id":"UC1","satisfied":true}],"fix_tasks":[]}`, nil
	}}
	critic := &fake{name: "critic", handler: func(req agent.Request, n int) (string, error) {
		return `{"verdict":"pass","summary":"ok"}`, nil
	}}
	e, d := newEngine(t, cfg, thinker, critic, builder)
	dir = d
	_, e.P.Spec, _ = parseSpec(specReply)
	e.P.Phase = state.PhaseBuilding
	e.P.TestCommand = "sh test.sh"
	e.P.Tasks = []*state.Task{{ID: "T1", Title: "one", Status: state.TaskActive, Attempts: 1}}
	if err := e.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if builds != 1 || e.P.Tasks[0].Status != state.TaskDone || e.P.Tasks[0].Attempts != 2 {
		t.Fatalf("builds=%d task=%+v", builds, e.P.Tasks[0])
	}
}

func TestNextTaskFallsBackOnCycles(t *testing.T) {
	e := &Engine{P: &state.Project{Tasks: []*state.Task{
		{ID: "A", Status: state.TaskPending, DependsOn: []string{"B"}},
		{ID: "B", Status: state.TaskPending, DependsOn: []string{"A"}},
	}}, Store: &state.Store{Root: t.TempDir()}, Out: io.Discard}
	if got := e.nextTask(); got == nil || got.ID != "A" {
		t.Fatalf("got %+v", got)
	}
}

func TestNormalizeTasks(t *testing.T) {
	in := []*state.Task{
		{ID: "T1", Title: "a", DependsOn: []string{"T9", "T1"}},
		{ID: "T1", Title: "dup"},
		{Title: ""},
		{Title: "no id", DependsOn: []string{"T1"}},
	}
	out := normalizeTasks(in, "T")
	if len(out) != 3 {
		t.Fatalf("len %d", len(out))
	}
	if out[0].DependsOn != nil || out[1].ID != "T2" || out[2].ID != "T4" || out[2].DependsOn[0] != "T1" {
		t.Fatalf("%+v %+v %+v", out[0], out[1], out[2])
	}
	if out[0].Kind != "feature" || out[0].Status != state.TaskPending {
		t.Fatal("defaults not applied")
	}
}
