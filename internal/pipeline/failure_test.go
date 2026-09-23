package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/state"
)

// The regression this exists for: wordcnt's attempt 1 and attempt 3 failed on
// the identical compile error, only differing in line numbers, and were
// treated as two unrelated problems.
func TestFailureSignatureGroupsTheSameMistake(t *testing.T) {
	attempt1 := "./run.go:17:21: cannot use os.Open (value of type func(name string) (*os.File, error)) as func(string) (io.ReadCloser, error) value in argument to runWithOpen\nFAIL	wordcnt [build failed]"
	attempt3 := "./run.go:42:9: cannot use os.Open (value of type func(name string) (*os.File, error)) as func(string) (io.ReadCloser, error) value in argument to runWithOpen\nFAIL	wordcnt [build failed]"
	if failureSignature(attempt1) != failureSignature(attempt3) {
		t.Errorf("the same compile error at different line numbers was not recognised as one failure:\n  %q\n  %q",
			failureSignature(attempt1), failureSignature(attempt3))
	}

	// A genuinely different failure must not collide with it.
	other := "--- FAIL: TestRunUC1Fixture (0.00s)\n    run_t3_test.go:67: fixture has len=4136 lines=120 words=640\nFAIL"
	if failureSignature(attempt1) == failureSignature(other) {
		t.Error("an unrelated test failure was grouped with the compile error")
	}
}

// Timings and counts change between runs; the mistake does not.
func TestFailureSignatureIgnoresVaryingDigits(t *testing.T) {
	a := "--- FAIL: TestX (1.23s)\n    x_test.go:67: expected 4096 bytes, got 4136"
	b := "--- FAIL: TestX (9.99s)\n    x_test.go:88: expected 4096 bytes, got 4136"
	if failureSignature(a) != failureSignature(b) {
		t.Errorf("digits made the same failure look different:\n  %q\n  %q",
			failureSignature(a), failureSignature(b))
	}
	if failureSignature("") != "" {
		t.Errorf("empty output = %q, want empty signature", failureSignature(""))
	}
}

func TestMostRepeatedAndRepeatNote(t *testing.T) {
	failures := []state.Failure{
		{Attempt: 1, Sig: "compile-error"},
		{Attempt: 2, Sig: "fixture-size"},
		{Attempt: 3, Sig: "compile-error"},
	}
	sig, n := mostRepeated(failures)
	if sig != "compile-error" || n != 2 {
		t.Fatalf("mostRepeated = (%q, %d), want (compile-error, 2)", sig, n)
	}

	// A repeat must be named, with the attempts, so the builder can see it
	// is regressing rather than hitting something new.
	note := repeatNote(failures, "compile-error")
	for _, want := range []string{"same failure", "2 times", "1, 3", "do not re-apply"} {
		if !strings.Contains(note, want) {
			t.Errorf("repeat note missing %q:\n%s", want, note)
		}
	}
	// A first-time failure gets no lecture.
	if got := repeatNote(failures, "fixture-size"); got != "" {
		t.Errorf("repeat note for a single failure = %q, want empty", got)
	}
	// Nothing repeated → nothing to say.
	if got := repeatNote(failures[:1], "compile-error"); got != "" {
		t.Errorf("repeat note after one occurrence = %q, want empty", got)
	}
}

func TestFailureHistoryListsAttempts(t *testing.T) {
	task := &state.Task{ID: "T3", Failures: []state.Failure{
		{Attempt: 1, Sig: "compile-error"},
		{Attempt: 3, Sig: "compile-error"},
	}}
	h := failureHistory(task)
	if !strings.Contains(h, "- attempt 1: `compile-error`") ||
		!strings.Contains(h, "- attempt 3: `compile-error`") {
		t.Errorf("history = %q", h)
	}
}

// gauge counts how many of a fake's calls overlap.
type gauge struct {
	inflight atomic.Int32
	maxSeen  atomic.Int32
	calls    atomic.Int32
}

func (g *gauge) handler(req agent.Request, n int) (string, error) {
	cur := g.inflight.Add(1)
	for {
		old := g.maxSeen.Load()
		if cur <= old || g.maxSeen.CompareAndSwap(old, cur) {
			break
		}
	}
	g.calls.Add(1)
	time.Sleep(40 * time.Millisecond) // wide enough that overlap is certain
	g.inflight.Add(-1)
	return "an opinion, for what it is worth", nil
}

// Roundtable participants fan out together. With a provider that allows two
// concurrent requests — the camelStream plan this project runs on — an
// uncapped fan-out queues against its own allowance, so the cap must hold
// while still letting every seat speak.
func TestRoundtableConcurrencyCap(t *testing.T) {
	agents := []config.Agent{
		{Type: "command", Command: "x"}, {Type: "command", Command: "x"},
		{Type: "command", Command: "x"}, {Type: "command", Command: "x"},
	}
	names := []string{"seat-a", "seat-b", "seat-c", "seat-d"}
	cfg := &config.Config{
		Agents: map[string]config.Agent{},
		Roles:  config.Roles{Moderator: "mod"},
		Roundtable: config.Roundtable{
			Rounds: 1,
			Participants: []config.Participant{
				{Agent: "seat-a", Persona: "engineer"},
				{Agent: "seat-b", Persona: "product"},
				{Agent: "seat-c", Persona: "qa"},
				{Agent: "seat-d", Persona: "skeptic"},
			},
		},
		Limits: config.Limits{MaxParallelParticipants: 2},
	}
	var fakes []*fake
	gauges := map[string]*gauge{}
	for i, name := range names {
		cfg.Agents[name] = agents[i]
		g := &gauge{}
		gauges[name] = g
		fakes = append(fakes, &fake{name: name, handler: g.handler})
	}
	cfg.Agents["mod"] = config.Agent{Type: "command", Command: "x"}
	mod := &fake{name: "mod", handler: func(agent.Request, int) (string, error) {
		return "brief as agreed", nil
	}}
	fakes = append(fakes, mod)

	e, _ := newEngine(t, cfg, fakes...)
	if _, err := e.Roundtable(context.Background(), "cap", "topic",
		"material", config.RoleModerator, "synthesise the brief"); err != nil {
		t.Fatalf("roundtable: %v", err)
	}

	var total int32
	for name, g := range gauges {
		if c := g.calls.Load(); c != 1 {
			t.Errorf("seat %s answered %d times, want 1", name, c)
		}
		if m := g.maxSeen.Load(); m > 2 {
			t.Errorf("seat overlap reached %d, want at most 2 (the provider's allowance)", m)
		}
		total += g.calls.Load()
	}
	if total != 4 {
		t.Errorf("only %d of 4 seats spoke — the cap must limit concurrency, not drop seats", total)
	}
}

// With no cap configured, behaviour is unchanged: everyone is still invited.
func TestRoundtableWithoutCapStillRunsAllSeats(t *testing.T) {
	cfg := &config.Config{
		Agents:     map[string]config.Agent{},
		Roles:      config.Roles{Moderator: "mod"},
		Roundtable: config.Roundtable{Rounds: 1},
		Limits:     config.Limits{}, // MaxParallelParticipants absent == 0 == uncapped
	}
	var fakes []*fake
	gauges := map[string]*gauge{}
	for _, name := range []string{"s1", "s2", "s3"} {
		cfg.Agents[name] = config.Agent{Type: "command", Command: "x"}
		g := &gauge{}
		gauges[name] = g
		fakes = append(fakes, &fake{name: name, handler: g.handler})
	}
	cfg.Agents["mod"] = config.Agent{Type: "command", Command: "x"}
	cfg.Roundtable.Participants = []config.Participant{
		{Agent: "s1", Persona: "a"}, {Agent: "s2", Persona: "b"}, {Agent: "s3", Persona: "c"},
	}
	fakes = append(fakes, &fake{name: "mod", handler: func(agent.Request, int) (string, error) {
		return "ok", nil
	}})

	e, _ := newEngine(t, cfg, fakes...)
	if _, err := e.Roundtable(context.Background(), "uncapped", "topic",
		"material", config.RoleModerator, "done"); err != nil {
		t.Fatalf("roundtable: %v", err)
	}
	for name, g := range gauges {
		if g.calls.Load() != 1 {
			t.Errorf("seat %s answered %d times, want 1", name, g.calls.Load())
		}
	}
}
