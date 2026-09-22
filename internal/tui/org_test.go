package tui

import (
	"strings"
	"testing"

	"github.com/dylan-demolder/factory/internal/config"
)

// newTestOrg mirrors a realistic roster: two editable agents and one
// openai-type agent, which factory forbids as the builder.
func newTestOrg() orgState {
	return orgState{
		agents: map[string]config.Agent{
			"aaa-opencode": {Type: "opencode", Model: "opencode-go/glm-5.3"},
			"mmm-openai":   {Type: "openai", BaseURL: "http://localhost:11434/v1", Model: "qwen3.7-max"},
			"zzz-command":  {Type: "command", Command: "python3"},
		},
		roles: [5]string{"aaa-opencode", "aaa-opencode", "aaa-opencode", "aaa-opencode", "aaa-opencode"},
		panel: []config.Participant{{Agent: "zzz-command", Persona: "pragmatic engineer"}},
		meta:  map[string]config.Meta{},
	}
}

func builderRow(t *testing.T, s orgState) orgRow {
	t.Helper()
	for _, r := range s.rows() {
		if r.kind == rowRole && roleKeys[r.i] == "builder" {
			return r
		}
	}
	t.Fatal("no builder row")
	return orgRow{}
}

// The one rule the org editor must never break: an openai-type agent cannot
// edit files, so it can never become the builder.
func TestOpenAIAgentNeverBecomesBuilder(t *testing.T) {
	s := newTestOrg()
	row := builderRow(t, s)

	// Cycle the full roster and back again; aaa → zzz → aaa … must skip mmm.
	for i := 0; i < 2*len(s.agents)+1; i++ {
		s.cycleAgent(row, 1)
		if s.roles[2] == "mmm-openai" {
			t.Fatalf("openai agent assigned as builder after %d cycles", i)
		}
		s.cycleAgent(row, -1)
		if s.roles[2] == "mmm-openai" {
			t.Fatalf("openai agent assigned as builder after %d reverse cycles", i)
		}
	}
	// The other agents must still be reachable — skipping cannot wedge cycling.
	if s.roles[2] == "aaa-opencode" {
		s.cycleAgent(row, 1)
	}
	if s.roles[2] == "aaa-opencode" {
		t.Error("cycling never advanced past the current builder")
	}
}

func TestCyclingRolesAndSeats(t *testing.T) {
	s := newTestOrg()

	roleRow := orgRow{kind: rowRole, i: 3} // reviewer
	before := s.roles[3]
	s.cycleAgent(roleRow, 1)
	if s.roles[3] == before {
		t.Error("cycling the reviewer role did not change its agent")
	}
	if !s.dirty {
		t.Error("a role change must mark the state dirty")
	}

	seatRow := orgRow{kind: rowSeat, i: 0}
	seatBefore := s.panel[0].Agent
	s.cycleAgent(seatRow, 1)
	if s.panel[0].Agent == seatBefore {
		t.Error("cycling seat 0 did not change its agent")
	}
	// Cycling must never rewrite the persona — that is edited separately.
	if s.panel[0].Persona != "pragmatic engineer" {
		t.Errorf("persona was overwritten: %q", s.panel[0].Persona)
	}
}

func TestRowsCoverRolesSeatsAndPool(t *testing.T) {
	s := newTestOrg() // zzz-command is seated, so it is not pooled

	var roles, seats, pool int
	for _, r := range s.rows() {
		switch r.kind {
		case rowRole:
			roles++
		case rowSeat:
			seats++
		case rowPool:
			pool++
		}
	}
	if roles != len(roleKeys) {
		t.Errorf("role rows = %d, want %d", roles, len(roleKeys))
	}
	if seats != 1 {
		t.Errorf("seat rows = %d, want 1", seats)
	}
	// aaa holds every role, zzz is seated: only mmm is unassigned.
	if pool != 1 {
		t.Errorf("pool rows = %d, want 1 (mmm-openai)", pool)
	}
	if got := s.poolNames(); len(got) != 1 || got[0] != "mmm-openai" {
		t.Errorf("poolNames = %v", got)
	}

	// Emptying the panel must return its agent to the pool, not orphan it.
	s.panel = nil
	if got := len(s.poolNames()); got != 2 {
		t.Errorf("pool after emptying the panel = %d, want 2", got)
	}
}

func TestInUseCoversRolesAndSeats(t *testing.T) {
	s := newTestOrg()
	if !s.inUse("aaa-opencode") {
		t.Error("an agent holding roles should be in use")
	}
	if !s.inUse("zzz-command") {
		t.Error("an agent seated on the panel should be in use")
	}
	if s.inUse("mmm-openai") {
		t.Error("an unassigned agent should not be in use")
	}
}

// typing() gates the root's esc/1-4/q. Getting it wrong means edits are
// discarded without a word.
func TestOrgTypingStates(t *testing.T) {
	s := newTestOrg()
	if s.typing() {
		t.Error("a clean, editor-free screen should not own the keyboard")
	}
	s.dirty = true
	if !s.typing() {
		t.Error("unsaved edits must block global shortcuts")
	}
	s.dirty = false
	s.edit = &orgEdit{}
	if !s.typing() {
		t.Error("an open inline editor must block global shortcuts")
	}
	s.edit = nil
	s.confirm = &orgConfirm{}
	if !s.typing() {
		t.Error("a pending confirmation must block global shortcuts")
	}
}

func TestEngineLineDescribesEachType(t *testing.T) {
	s := newTestOrg()
	if got := s.engineLine("aaa-opencode"); !strings.Contains(got, "OpenCode Go") {
		t.Errorf("opencode line = %q", got)
	}
	if got := s.engineLine("mmm-openai"); !strings.Contains(got, "openai") && !strings.Contains(got, "OpenAI") {
		t.Errorf("openai line = %q", got)
	}
	if got := s.engineLine("zzz-command"); !strings.Contains(got, "python3") {
		t.Errorf("command line = %q", got)
	}
}

func TestMetaReadOnlyFollowsRole(t *testing.T) {
	s := newTestOrg()
	s.roles[3] = "aaa-opencode" // reviewer
	if !s.fillsReadOnlyRole("aaa-opencode") {
		t.Error("an agent holding the reviewer role should be read-only")
	}
	// mmm holds nothing at all.
	if s.fillsReadOnlyRole("mmm-openai") {
		t.Error("an agent holding no role should not be marked read-only")
	}
	// Clear every role and the read-only marking has to go with them.
	for i := range s.roles {
		s.roles[i] = ""
	}
	if s.fillsReadOnlyRole("aaa-opencode") {
		t.Error("an agent that no longer holds a read-only role should not stay read-only")
	}
}

func TestDoctorTally(t *testing.T) {
	d := doctorState{results: []doctorResult{{ok: true}, {ok: false}, {ok: true}}}
	ok, total := d.tally()
	if ok != 2 || total != 3 {
		t.Errorf("tally = %d/%d, want 2/3", ok, total)
	}
	empty := doctorState{}
	if ok, total := empty.tally(); ok != 0 || total != 0 {
		t.Errorf("empty tally = %d/%d", ok, total)
	}
}
