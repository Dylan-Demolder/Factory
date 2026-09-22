package tui

// The org chart editor: which agent fills each role, who sits on the
// roundtable, and each agent's human metadata. It edits two files through
// config.Save and never writes anything itself.
//
// The document being edited is the *raw* config (map[string]any straight off
// disk), not the parsed *config.Config: Parse expands {{config_dir}} in args
// and command, so saving the parsed form would freeze absolute paths into the
// user's config. The parsed config is kept alongside for the things only it
// knows — each agent's real type and the applied defaults (rounds, filled-in
// roles).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/dylan-demolder/factory/internal/config"
)

// roleKeys are factory's five roles in config.Roles' field order. They are
// fixed; the pipeline addresses them by name.
var roleKeys = []string{"interviewer", "planner", "builder", "reviewer", "moderator"}

// readOnlyRoles never edit files, so rows carrying them are marked read-only.
var readOnlyRoles = map[string]bool{"interviewer": true, "reviewer": true}

// orgDepts are the departments in use across the org. A pool row cycles
// through them with ←/→; the inline editor accepts anything else.
var orgDepts = []string{"exec", "spec", "plan", "eng", "qa", "panel"}

// orgTypes are the engines a new agent may be created with.
var orgTypes = []string{"opencode", "command", "openai"}

// orgLoadedMsg carries the config into the screen: the parsed form (real
// types, defaults applied) plus the raw document edits are folded back into.
type orgLoadedMsg struct {
	cfg  *config.Config
	path string
	doc  map[string]any
	meta map[string]config.Meta
	err  error
}

// orgSavedMsg is config.Save's verdict. A rejected document carries
// per-field messages (roles.builder, agents.x.model, …) via config.Fields.
type orgSavedMsg struct{ err error }

// orgState is the editor's working set: three copies in memory (roles, panel,
// agents) folded back into doc only when the user saves, so an abandoned
// session never touches disk.
type orgState struct {
	loaded bool
	err    error

	path string                 // resolved config file — where Save writes
	cfg  *config.Config         // parsed: types, rounds, defaults
	doc  map[string]any         // raw document, edited on save
	meta map[string]config.Meta // sidecar metadata, saved with the config

	roles  [5]string            // working copy of the roles, roleKeys order
	panel  []config.Participant // working copy of the seats, in order
	agents map[string]config.Agent

	created     map[string]bool // added here: fresh objects under doc["agents"]
	modelEdited map[string]bool // model typed over: written back on save

	cursor    int
	dirty     bool
	fieldErrs []config.FieldError

	edit    *orgEdit    // inline text editor; nil when the list has focus
	confirm *orgConfirm // pending y/n question; nil otherwise
}

// typing reports that this screen owns the keyboard. Three states qualify:
// an inline editor is open (keystrokes are text), a confirmation is pending
// (y/n must not be read as navigation), and there are unsaved edits — so the
// root's esc/1-4/q cannot whisk changes away without a word of warning.
func (s orgState) typing() bool { return s.edit != nil || s.confirm != nil || s.dirty }

// apply replaces the state with what was loaded, keeping the cursor where the
// user left it. Loading is also how a discard undoes: the next visit re-reads
// disk.
func (s *orgState) apply(msg orgLoadedMsg) {
	cursor := s.cursor
	*s = orgState{cursor: cursor}
	if msg.err != nil {
		s.err = msg.err
		return
	}
	if msg.cfg == nil || msg.path == "" {
		s.err = fmt.Errorf("config missing after resolve")
		return
	}
	s.loaded = true
	s.path, s.cfg, s.doc, s.meta = msg.path, msg.cfg, msg.doc, msg.meta
	if s.doc == nil {
		s.doc = map[string]any{}
	}
	if s.meta == nil {
		s.meta = map[string]config.Meta{}
	}
	r := s.cfg.Roles
	s.roles = [5]string{r.Interviewer, r.Planner, r.Builder, r.Reviewer, r.Moderator}
	s.panel = append([]config.Participant(nil), s.cfg.Roundtable.Participants...)
	s.agents = make(map[string]config.Agent, len(s.cfg.Agents))
	for name, a := range s.cfg.Agents {
		s.agents[name] = a
	}
}

// ---- loading ----

// loadOrg resolves the config and reads the raw file beside it. Both halves
// are needed: the parsed config knows each agent's real type and the applied
// defaults, while the raw document is what edits are folded back into so
// {{config_dir}} placeholders survive a save.
func (m Model) loadOrg() tea.Cmd {
	wanted := m.CfgPath
	return func() tea.Msg {
		cfg, path, err := config.Resolve(wanted, "")
		if err != nil {
			return orgLoadedMsg{err: err}
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return orgLoadedMsg{err: fmt.Errorf("%s: %w", path, err)}
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return orgLoadedMsg{err: fmt.Errorf("%s: %w", path, err)}
		}
		return orgLoadedMsg{cfg: cfg, path: path, doc: doc, meta: config.LoadMeta(path)}
	}
}

// ---- rows ----

type orgRowKind int

const (
	rowRole orgRowKind = iota
	rowSeat
	rowPool
)

// orgRow is one cursor stop. Section headers are not rows: the cursor walks
// only roles, seats and pooled agents, so ↑/↓ never lands on decoration.
type orgRow struct {
	kind orgRowKind
	i    int
}

// rows is the cursor order: the five roles, then seats in panel order, then
// every agent holding neither.
func (s orgState) rows() []orgRow {
	rows := make([]orgRow, 0, len(roleKeys)+len(s.panel)+len(s.agents))
	for i := range roleKeys {
		rows = append(rows, orgRow{kind: rowRole, i: i})
	}
	for i := range s.panel {
		rows = append(rows, orgRow{kind: rowSeat, i: i})
	}
	for i := range s.poolNames() {
		rows = append(rows, orgRow{kind: rowPool, i: i})
	}
	return rows
}

// poolNames lists agents with no role and no seat, alphabetically.
func (s orgState) poolNames() []string {
	names := s.agentNames()
	out := make([]string, 0, len(names))
	for _, n := range names {
		if !s.inUse(n) {
			out = append(out, n)
		}
	}
	return out
}

// inUse reports whether an agent holds a role or a seat.
func (s orgState) inUse(name string) bool {
	for _, r := range s.roles {
		if r == name {
			return true
		}
	}
	for _, p := range s.panel {
		if p.Agent == name {
			return true
		}
	}
	return false
}

// agentNames is the roster in sorted order — cycling ←/→ walks it.
func (s orgState) agentNames() []string {
	names := make([]string, 0, len(s.agents))
	for n := range s.agents {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// rowAgent is the agent a row is about; "" when there is none (empty role).
func (s orgState) rowAgent(row orgRow) string {
	switch row.kind {
	case rowRole:
		if row.i < len(roleKeys) {
			return s.roles[row.i]
		}
	case rowSeat:
		if row.i < len(s.panel) {
			return s.panel[row.i].Agent
		}
	case rowPool:
		pool := s.poolNames()
		if row.i < len(pool) {
			return pool[row.i]
		}
	}
	return ""
}

// rowIndex finds a row's cursor position, so the cursor can follow a seat
// that moves or appears.
func (s orgState) rowIndex(kind orgRowKind, i int) int {
	pos := 0
	for _, r := range s.rows() {
		if r.kind == kind && r.i == i {
			return pos
		}
		pos++
	}
	return -1
}

func (s *orgState) clampCursor() {
	n := len(s.rows())
	if s.cursor > n-1 {
		s.cursor = max(0, n-1)
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

// rolesOf / seatsOf back the delete warning: removing an agent that is
// currently working has consequences worth seeing before confirming.
func (s orgState) rolesOf(name string) []string {
	var out []string
	for i, r := range s.roles {
		if r == name {
			out = append(out, roleKeys[i])
		}
	}
	return out
}

func (s orgState) seatsOf(name string) int {
	n := 0
	for _, p := range s.panel {
		if p.Agent == name {
			n++
		}
	}
	return n
}

// ---- update ----

func (m Model) updateOrg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case orgLoadedMsg:
		m.org.apply(msg)
		return m, nil
	case orgSavedMsg:
		return m.orgSaved(msg)
	case tea.KeyMsg:
		return m.orgKey(msg)
	}
	return m, nil
}

// orgSaved clears the dirty flag or pins each rejection to the row that
// caused it. config.Fields gives exact paths (roles.builder, agents.x.model,
// roundtable.participants.1.persona): those stay beside the offending row
// until the next successful save, while the status line carries the summary.
func (m Model) orgSaved(msg orgSavedMsg) (tea.Model, tea.Cmd) {
	s := &m.org
	if msg.err == nil {
		s.dirty = false
		s.fieldErrs = nil
		s.created, s.modelEdited = nil, nil
		return m, sayStatus("saved "+s.path, false)
	}
	if fields := config.Fields(msg.err); fields != nil {
		s.fieldErrs = fields
		text := strings.ReplaceAll(fields[0].Msg, "\n", " ")
		if len(fields) > 1 {
			text = fmt.Sprintf("%d problems — first: %s", len(fields), text)
		}
		return m, sayStatus(text, true)
	}
	return m, sayStatus(strings.ReplaceAll(msg.err.Error(), "\n", " "), true)
}

// orgKey hands the key to whichever part of the screen has focus. The inline
// editor and pending confirmations take every key, so a global shortcut never
// steals a keystroke mid-word (or a "y"). Every branch returns the model it
// mutated: these are value receivers, and dropping the copy would drop the edit.
func (m Model) orgKey(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.org.edit != nil {
		return m.orgEditKey(km)
	}
	if m.org.confirm != nil {
		return m.orgConfirmKey(km)
	}
	return m.orgListKey(km)
}

// orgListKey drives the chart itself:
//
//	↑↓ / jk   move the cursor      ←→ / hl   change the selected value
//	⏎         edit the agent       p         edit a seat's persona
//	+         add a seat           x         remove the seat
//	[ ] / shift+←→  reorder seats   a         new agent (on a seat: add one)
//	d         delete the agent      s         save      esc  back out
func (m Model) orgListKey(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.org
	s.clampCursor()
	rows := s.rows()
	var row orgRow
	if len(rows) > 0 {
		row = rows[s.cursor]
	}

	var cmd tea.Cmd
	switch km.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(rows)-1 {
			s.cursor++
		}
	case "left", "h":
		cmd = s.cycleRow(row, -1)
	case "right", "l":
		cmd = s.cycleRow(row, +1)
	case "shift+left", "[":
		cmd = s.moveSeat(row, -1)
	case "shift+right", "]":
		cmd = s.moveSeat(row, +1)
	case "enter":
		cmd = s.openOrgEdit(row)
	case "p":
		cmd = s.openPersonaEdit(row)
	case "+":
		cmd = s.addSeat(row)
	case "a":
		// On a seat "+/a" adds a seat; everywhere else "a" is the new agent.
		if row.kind == rowSeat {
			cmd = s.addSeat(row)
		} else {
			cmd = s.openNewAgent()
		}
	case "x":
		cmd = s.removeSeat(row)
	case "d":
		cmd = s.deleteAgentPrompt(row)
	case "s":
		cmd = s.saveOrg()
	case "esc":
		// Reachable only while typing() is true — i.e. while dirty, since the
		// root takes a clean esc — which means there is something to lose.
		if s.dirty {
			s.confirm = &orgConfirm{kind: confirmDiscard, prompt: "Discard unsaved changes?"}
		} else {
			m.route = routeHome
			cmd = loadProjects(m.Workspace)
		}
	case "?":
		// Also only reachable while dirty; toggling help loses nothing.
		m.showHelp = !m.showHelp
	case "1", "2", "3", "4", "q":
		// These reach the screen only while typing() is true, i.e. while
		// dirty: navigation (or quitting) that would drop the edits gets a
		// warning instead of silence.
		if s.dirty {
			cmd = sayStatus("unsaved changes — s to save, esc to discard", true)
		}
	}
	return m, cmd
}

// cycleRow changes the selected row's value: roles and seats cycle their
// agent, pool rows cycle the department.
func (s *orgState) cycleRow(row orgRow, dir int) tea.Cmd {
	if row.kind == rowPool {
		return s.cycleDept(row, dir)
	}
	return s.cycleAgent(row, dir)
}

// cycleAgent walks the roster. The builder is special: an openai agent
// cannot edit files, so those candidates are skipped — and the skip is
// announced, never silent, so nobody wonders why their pick never lands.
func (s *orgState) cycleAgent(row orgRow, dir int) tea.Cmd {
	names := s.agentNames()
	if len(names) == 0 {
		return sayStatus("no agents — press a to add one", true)
	}
	cur := s.rowAgent(row)
	start := orgIndexOf(names, cur)
	skipped, pick := false, -1
	for step := 1; step <= len(names); step++ {
		idx := 0
		switch {
		case start >= 0:
			idx = start + dir*step
		case dir > 0:
			idx = step - 1
		default:
			idx = len(names) - step
		}
		for idx < 0 {
			idx += len(names)
		}
		idx %= len(names)
		cand := names[idx]
		if cand == cur {
			break // full circle; nothing else to offer
		}
		if row.kind == rowRole && roleKeys[row.i] == "builder" && s.agents[cand].Type == "openai" {
			skipped = true
			continue
		}
		pick = idx
		break
	}
	const openaiReason = "openai agents cannot be the builder: they cannot edit files"
	if pick < 0 {
		if skipped {
			return sayStatus(openaiReason, true)
		}
		return sayStatus("only one agent to choose from", false)
	}
	s.assign(row, names[pick])
	if skipped {
		return sayStatus(openaiReason, true)
	}
	return nil
}

// assign points a role or seat at an agent.
func (s *orgState) assign(row orgRow, name string) {
	switch row.kind {
	case rowRole:
		if row.i < len(roleKeys) {
			s.roles[row.i] = name
			s.dirty = true
		}
	case rowSeat:
		if row.i < len(s.panel) {
			s.panel[row.i].Agent = name
			s.dirty = true
		}
	}
}

// cycleDept is ←/→ on a pool row: the department is the one field there with
// a fixed vocabulary worth stepping through.
func (s *orgState) cycleDept(row orgRow, dir int) tea.Cmd {
	name := s.rowAgent(row)
	if name == "" {
		return nil
	}
	md := s.meta[name]
	next := orgDepts[0]
	switch i := orgIndexOf(orgDepts, md.Dept); {
	case i >= 0:
		next = orgDepts[(i+dir+len(orgDepts))%len(orgDepts)]
	case dir < 0:
		next = orgDepts[len(orgDepts)-1]
	}
	md.Dept = next
	s.meta[name] = md
	s.dirty = true
	return nil
}

// moveSeat reorders the panel: [ / shift+← lifts the seat, ] / shift+→ drops
// it. Seats are an ordered list, so the cursor follows the seat it moved.
func (s *orgState) moveSeat(row orgRow, dir int) tea.Cmd {
	if row.kind != rowSeat {
		return sayStatus("[ and ] reorder seats — select a seat", true)
	}
	j := row.i + dir
	if row.i >= len(s.panel) || j < 0 || j >= len(s.panel) {
		return nil
	}
	s.panel[row.i], s.panel[j] = s.panel[j], s.panel[row.i]
	s.dirty = true
	if pos := s.rowIndex(rowSeat, j); pos >= 0 {
		s.cursor = pos
	}
	return nil
}

// addSeat inserts a seat. On an existing seat it duplicates that seat right
// after itself (same agent and persona, ready to retarget with ←/→); on a
// role or pool row it seats that row's agent at the end of the panel. Seats
// added from a role or pool start with no persona and say so, so the hole is
// visible instead of silent.
func (s *orgState) addSeat(row orgRow) tea.Cmd {
	names := s.agentNames()
	if len(names) == 0 {
		return sayStatus("no agents — press a to add one", true)
	}
	agent, persona := s.rowAgent(row), ""
	at := len(s.panel)
	if row.kind == rowSeat && row.i < len(s.panel) {
		agent, persona = s.panel[row.i].Agent, s.panel[row.i].Persona
		at = row.i + 1
	}
	if agent == "" {
		agent = names[0]
	}
	s.panel = append(s.panel, config.Participant{})
	copy(s.panel[at+1:], s.panel[at:])
	s.panel[at] = config.Participant{Agent: agent, Persona: persona}
	s.dirty = true
	if pos := s.rowIndex(rowSeat, at); pos >= 0 {
		s.cursor = pos
	}
	return nil
}

// removeSeat takes a seat away; the last one asks first, because an empty
// roundtable changes how the pipeline behaves (the moderator decides alone).
func (s *orgState) removeSeat(row orgRow) tea.Cmd {
	if row.kind != rowSeat || row.i >= len(s.panel) {
		return sayStatus("x removes a seat — select one", true)
	}
	if len(s.panel) == 1 {
		s.confirm = &orgConfirm{
			kind:   confirmRemoveSeat,
			seat:   row.i,
			prompt: "Remove the last seat? The roundtable will be empty — the moderator decides alone.",
		}
		return nil
	}
	s.dropSeat(row.i)
	return nil
}

func (s *orgState) dropSeat(i int) {
	if i < 0 || i >= len(s.panel) {
		return
	}
	s.panel = append(s.panel[:i], s.panel[i+1:]...)
	s.dirty = true
	s.clampCursor()
}

// deleteAgentPrompt asks before removing an agent, spelling out any role or
// seat it currently holds — those are cleared, and the roles will need
// reassigning before the config can be saved again.
func (s *orgState) deleteAgentPrompt(row orgRow) tea.Cmd {
	name := s.rowAgent(row)
	if name == "" {
		return sayStatus("no agent on this row — press a to add one", true)
	}
	prompt := fmt.Sprintf("Delete agent %q?", name)
	roles, seats := s.rolesOf(name), s.seatsOf(name)
	if len(roles) > 0 {
		prompt += " Holds role(s): " + strings.Join(roles, ", ")
	}
	if seats > 0 {
		prompt += fmt.Sprintf(", and %d seat(s)", seats)
	}
	if len(roles) > 0 || seats > 0 {
		prompt += " — cleared on delete; reassign roles before saving."
	}
	s.confirm = &orgConfirm{kind: confirmDeleteAgent, agent: name, prompt: prompt}
	return nil
}

// dropAgent removes an agent from the roster, its seats and its roles. Roles
// become empty rather than silently reassigned: config.Save will then pin
// "role X: no agent assigned" to the exact row, which beats magic.
func (s *orgState) dropAgent(name string) tea.Cmd {
	roles := s.rolesOf(name)
	delete(s.agents, name)
	delete(s.created, name)
	delete(s.modelEdited, name)
	for i, r := range s.roles {
		if r == name {
			s.roles[i] = ""
		}
	}
	kept := s.panel[:0]
	for _, p := range s.panel {
		if p.Agent != name {
			kept = append(kept, p)
		}
	}
	s.panel = kept
	s.dirty = true
	s.clampCursor()
	if len(roles) > 0 {
		return sayStatus(fmt.Sprintf("deleted %s — reassign %s before saving", name, strings.Join(roles, ", ")), true)
	}
	return sayStatus("deleted "+name+" — press s to save", false)
}

// ---- saving ----

// saveOrg folds the working copies into the raw document and hands both to
// config.Save on a command goroutine. Save is the only writer: it validates,
// keeps placeholders symbolic, inherits withheld env secrets and locks the
// file — none of that is reimplemented here.
func (s *orgState) saveOrg() tea.Cmd {
	if !s.loaded || s.path == "" {
		return sayStatus("no config to save", true)
	}
	if !s.dirty {
		return sayStatus("no changes to save", false)
	}
	doc := s.editedDoc()
	path, meta := s.path, s.meta
	return func() tea.Msg { return orgSavedMsg{err: config.Save(path, doc, meta)} }
}

// editedDoc writes the three working copies back into the raw document:
// roles wholesale, participants under the existing roundtable object (so
// rounds/on_spec survive), agents by add/remove per name. Pre-existing agent
// objects are left alone except a model this editor changed — their args and
// command still hold {{config_dir}}, and rewriting them from the parsed form
// would freeze absolute paths into the file.
func (s orgState) editedDoc() map[string]any {
	doc := s.doc
	if doc == nil {
		doc = map[string]any{}
	}

	roles := map[string]any{}
	for i, k := range roleKeys {
		if s.roles[i] != "" {
			roles[k] = s.roles[i]
		}
	}
	doc["roles"] = roles

	rt, _ := doc["roundtable"].(map[string]any)
	if rt == nil {
		rt = map[string]any{}
	}
	parts := make([]any, 0, len(s.panel))
	for _, p := range s.panel {
		parts = append(parts, map[string]any{"agent": p.Agent, "persona": p.Persona})
	}
	rt["participants"] = parts
	doc["roundtable"] = rt

	agents, _ := doc["agents"].(map[string]any)
	if agents == nil {
		agents = map[string]any{}
		doc["agents"] = agents
	}
	for name := range agents {
		if _, ok := s.agents[name]; !ok {
			delete(agents, name)
		}
	}
	for name, a := range s.agents {
		obj, ok := agents[name].(map[string]any)
		if !ok || s.created[name] {
			agents[name] = orgAgentObject(a)
			continue
		}
		if s.modelEdited[name] {
			obj["model"] = a.Model
		}
	}
	return doc
}

// orgAgentObject builds a fresh agents-map entry for an agent created in the
// editor. Only fields the form collected are written; nothing is invented,
// and a command typed here may itself contain {{config_dir}}.
func orgAgentObject(a config.Agent) map[string]any {
	obj := map[string]any{"type": a.Type}
	if a.Model != "" {
		obj["model"] = a.Model
	}
	if a.Command != "" {
		obj["command"] = a.Command
	}
	if a.BaseURL != "" {
		obj["base_url"] = a.BaseURL
	}
	return obj
}

// ---- inline editor ----

type orgEditKind int

const (
	editMeta    orgEditKind = iota // title, dept, tier, notes, model
	editNew                        // name, type, model, command, base_url
	editPersona                    // one seat's persona
)

type orgEditField struct {
	label       string
	placeholder string
	limit       int
}

// orgEdit is the mini text editor drawn under the list. One input is reused
// for every field: committing a field stores it and retargets the input, so
// focus never leaves and no keystroke can escape to global shortcuts.
type orgEdit struct {
	kind   orgEditKind
	agent  string // editMeta target
	seat   int    // editPersona target
	fields []orgEditField
	vals   []string // committed values; vals[sel] is what the input shows
	sel    int
	input  textinput.Model
}

func newOrgEdit(kind orgEditKind, agent string, seat int, fields []orgEditField, vals []string) *orgEdit {
	e := &orgEdit{kind: kind, agent: agent, seat: seat, fields: fields, vals: vals, input: textinput.New()}
	e.input.Prompt = accentStyle.Render("› ")
	e.input.Width = 46
	e.show(0)
	e.input.Focus()
	return e
}

// show targets the input at sel.
func (e *orgEdit) show(sel int) {
	e.sel = sel
	f := e.fields[sel]
	e.input.CharLimit = f.limit
	e.input.Placeholder = f.placeholder
	e.input.SetValue(e.vals[sel])
	e.input.CursorEnd()
}

// openOrgEdit opens the metadata editor for the selected row's agent: the
// sidecar fields (title, dept, tier, notes) plus model — the one engine field
// that can be written back without touching placeholders.
func (s *orgState) openOrgEdit(row orgRow) tea.Cmd {
	name := s.rowAgent(row)
	if name == "" {
		return sayStatus("no agent here — ←/→ picks one", true)
	}
	a, md := s.agents[name], s.meta[name]
	s.edit = newOrgEdit(editMeta, name, -1, []orgEditField{
		{label: "title", placeholder: "Staff engineer", limit: 80},
		{label: "dept", placeholder: strings.Join(orgDepts, " · "), limit: 40},
		{label: "tier", placeholder: "senior · lead · contractor", limit: 40},
		{label: "notes", placeholder: "what the team should know", limit: 240},
		{label: "model", placeholder: "empty = engine default", limit: 160},
	}, []string{md.Title, md.Dept, md.Tier, md.Notes, a.Model})
	return nil
}

// openPersonaEdit edits a seat's persona — the perspective that seat brings
// to every roundtable.
func (s *orgState) openPersonaEdit(row orgRow) tea.Cmd {
	if row.kind != rowSeat || row.i >= len(s.panel) {
		return sayStatus("p edits a seat's persona — select a seat", true)
	}
	s.edit = newOrgEdit(editPersona, "", row.i, []orgEditField{
		{label: "persona", placeholder: "how this seat sees every question", limit: 500},
	}, []string{s.panel[row.i].Persona})
	return nil
}

// openNewAgent starts the new-agent form: name, engine, model, command and
// base_url — everything a saveable agent of any of the three types needs.
func (s *orgState) openNewAgent() tea.Cmd {
	s.edit = newOrgEdit(editNew, "", -1, []orgEditField{
		{label: "name", placeholder: "oc-qa", limit: 64},
		{label: "type", placeholder: strings.Join(orgTypes, " · "), limit: 16},
		{label: "model", placeholder: "empty = engine default", limit: 160},
		{label: "command", placeholder: "e.g. python3 (type command)", limit: 200},
		{label: "base_url", placeholder: "https://… (type openai)", limit: 200},
	}, []string{"", "opencode", "", "", ""})
	return nil
}

// orgEditKey drives the inline editor: enter commits the current field and
// moves on (on the last field it finishes), esc closes without committing the
// field being typed, everything else is text.
func (m Model) orgEditKey(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.org
	e := s.edit
	if e == nil {
		return m, nil
	}
	var cmd tea.Cmd
	switch km.String() {
	case "esc":
		s.edit = nil // fields already committed with enter stay; this one is dropped
	case "enter", "tab":
		cmd = e.commit(s)
	default:
		e.input, cmd = e.input.Update(km)
	}
	return m, cmd
}

// commit stores the field being typed. Existing targets apply immediately —
// enter means "commit the field" — while the new-agent form only finishes on
// its last field, so a half-typed agent never exists.
func (e *orgEdit) commit(s *orgState) tea.Cmd {
	e.vals[e.sel] = strings.TrimSpace(e.input.Value())
	switch e.kind {
	case editMeta:
		return e.applyMeta(s)
	case editPersona:
		if e.seat >= 0 && e.seat < len(s.panel) {
			s.panel[e.seat].Persona = e.vals[0]
			s.dirty = true
		}
		s.edit = nil
		return sayStatus("persona updated — press s to save", false)
	}
	if e.sel < len(e.fields)-1 {
		e.show(e.sel + 1)
		return nil
	}
	return e.finishNew(s)
}

// applyMeta writes the committed field into the sidecar (or, for model, into
// the working agent — model is safe to write back; args and command are not,
// because Parse expands them).
func (e *orgEdit) applyMeta(s *orgState) tea.Cmd {
	name := e.agent
	md := s.meta[name]
	switch e.sel {
	case 0:
		md.Title = e.vals[0]
	case 1:
		md.Dept = e.vals[1]
	case 2:
		md.Tier = e.vals[2]
	case 3:
		md.Notes = e.vals[3]
	case 4:
		if a, ok := s.agents[name]; ok {
			a.Model = e.vals[4]
			s.agents[name] = a
			if s.modelEdited == nil {
				s.modelEdited = map[string]bool{}
			}
			s.modelEdited[name] = true
		}
	}
	s.meta[name] = md
	s.dirty = true
	if e.sel < len(e.fields)-1 {
		e.show(e.sel + 1)
		return nil
	}
	s.edit = nil
	return sayStatus("edited "+name+" — press s to save", false)
}

// finishNew validates the form and creates the agent in the working set. A
// mistake keeps the form open with the offending field re-selected, so the
// fix is one keystroke away.
func (e *orgEdit) finishNew(s *orgState) tea.Cmd {
	fail := func(sel int, msg string) tea.Cmd {
		e.show(sel)
		return sayStatus(msg, true)
	}
	name := e.vals[0]
	typ := e.vals[1]
	model, command, base := e.vals[2], e.vals[3], e.vals[4]
	if name == "" {
		return fail(0, "the agent needs a name")
	}
	if _, dup := s.agents[name]; dup {
		return fail(0, fmt.Sprintf("agent %q already exists", name))
	}
	if orgIndexOf(orgTypes, typ) < 0 {
		return fail(1, "type must be opencode, command or openai")
	}
	if typ == "command" && command == "" {
		return fail(3, "a command agent needs a command")
	}
	if typ == "openai" && model == "" {
		return fail(2, "an openai agent needs a model")
	}
	if typ == "openai" && base == "" {
		return fail(4, "an openai agent needs a base_url")
	}
	s.agents[name] = config.Agent{Type: typ, Model: model, Command: command, BaseURL: base}
	if s.created == nil {
		s.created = map[string]bool{}
	}
	s.created[name] = true
	s.dirty = true
	s.edit = nil
	for i, n := range s.poolNames() {
		if n == name {
			if pos := s.rowIndex(rowPool, i); pos >= 0 {
				s.cursor = pos
			}
			break
		}
	}
	return sayStatus("added "+name+" — press s to save", false)
}

// ---- confirmations ----

type orgConfirmKind int

const (
	confirmDiscard orgConfirmKind = iota
	confirmRemoveSeat
	confirmDeleteAgent
)

// orgConfirm is a y/n question. It counts as typing() so no stray global key
// can answer it or navigate away underneath it.
type orgConfirm struct {
	kind   orgConfirmKind
	prompt string
	seat   int    // confirmRemoveSeat
	agent  string // confirmDeleteAgent
}

func (m Model) orgConfirmKey(km tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := &m.org
	c := s.confirm
	if c == nil {
		return m, nil
	}
	var cmd tea.Cmd
	switch km.String() {
	case "y":
		s.confirm = nil
		switch c.kind {
		case confirmDiscard:
			m.org = orgState{}
			m.route = routeHome
			cmd = loadProjects(m.Workspace)
		case confirmRemoveSeat:
			s.dropSeat(c.seat)
			cmd = sayStatus("seat removed — press s to save", false)
		case confirmDeleteAgent:
			cmd = s.dropAgent(c.agent)
		}
	case "n", "esc":
		s.confirm = nil
	}
	// Anything else is ignored: only y/n/esc answer a question.
	return m, cmd
}

// ---- view ----

func (m Model) viewOrg(w, h int) string {
	s := m.org
	frame := lipgloss.NewStyle().Padding(1, 2).Width(w).Height(h)
	switch {
	case s.err != nil:
		return frame.Render(m.orgErrorView(w))
	case !s.loaded:
		return frame.Render(
			h1Style.Render("Org chart") + "\n\n" +
				mutedStyle.Render("  "+m.spinner.View()+" loading config…"))
	}
	return frame.Render(m.orgBody(w, h))
}

// orgErrorView is the "no config found" (or config too broken to resolve)
// state: what went wrong and the one command that fixes it.
func (m Model) orgErrorView(w int) string {
	var b strings.Builder
	b.WriteString(h1Style.Render("Org chart") + "\n\n")
	msg := strings.ReplaceAll(m.org.err.Error(), "\n", "  ")
	if strings.Contains(msg, "no config found") {
		b.WriteString("  " + titleStyle.Render("No config found") + "\n")
		b.WriteString("  " + mutedStyle.Render("factory edits factory.json — run `factory init` to create one.") + "\n")
	} else {
		b.WriteString("  " + badStyle.Render(truncate(msg, max(10, w-4))) + "\n")
	}
	b.WriteString("\n  " + mutedStyle.Render("press 3 to retry · esc back"))
	return b.String()
}

// orgBody renders the three sections. The list scrolls around the cursor when
// it does not fit, so the selected row is never off-screen; the editor,
// confirmation and key hint stay pinned below it.
func (m Model) orgBody(w, h int) string {
	s := &m.org
	s.clampCursor()

	pills := ""
	if s.dirty {
		pills += " " + pill(colorWarn, "unsaved")
	}
	if len(s.fieldErrs) > 0 {
		pills += " " + pill(colorBad, fmt.Sprintf("%d issue(s)", len(s.fieldErrs)))
	}
	head := []string{
		h1Style.Render("Org chart") + pills,
		mutedStyle.Render(truncate(fmt.Sprintf("%s · %d agents", s.path, len(s.agents)), max(10, w-4))),
	}

	var mid []string
	curMid := -1
	add := func(text string, cursorOn bool) {
		if cursorOn {
			curMid = len(mid)
		}
		mid = append(mid, text)
	}
	addDetail := func(row orgRow, sel bool) {
		if !sel {
			return
		}
		if d := s.orgDetail(w, row); d != "" {
			mid = append(mid, d)
		}
	}

	seatBase := len(roleKeys)
	poolBase := seatBase + len(s.panel)

	mid = append(mid, sectionStyle.Render("Roles"))
	for i := range roleKeys {
		sel := s.cursor == i
		add(s.orgRoleLine(w, i, sel), sel)
		addDetail(orgRow{kind: rowRole, i: i}, sel)
	}

	rounds := 2
	if s.cfg != nil && s.cfg.Roundtable.Rounds > 0 {
		rounds = s.cfg.Roundtable.Rounds
	}
	cost := len(s.panel)*rounds + 1
	mid = append(mid, sectionStyle.Render("Roundtable")+" "+pill(colorInfo, fmt.Sprintf("%d calls/table", cost)))
	mid = append(mid, "  "+mutedStyle.Render(fmt.Sprintf("%d seat(s) × %d round(s) + 1 = ", len(s.panel), rounds))+
		infoStyle.Render(fmt.Sprintf("%d agent calls per table", cost)))
	if len(s.panel) == 0 {
		mid = append(mid, mutedStyle.Render("  no seats — the moderator decides alone · + to add one"))
	}
	for i := range s.panel {
		sel := s.cursor == seatBase+i
		add(s.orgSeatLine(w, i, sel), sel)
		addDetail(orgRow{kind: rowSeat, i: i}, sel)
	}

	mid = append(mid, sectionStyle.Render("Pool"))
	pool := s.poolNames()
	switch {
	case len(s.agents) == 0:
		mid = append(mid, mutedStyle.Render("  no agents configured — press a to add one"))
	case len(pool) == 0:
		mid = append(mid, mutedStyle.Render("  everyone has a role or a seat."))
	}
	for i := range pool {
		sel := s.cursor == poolBase+i
		add(s.orgPoolLine(w, i, sel), sel)
		addDetail(orgRow{kind: rowPool, i: i}, sel)
	}

	tail := []string{}
	if s.edit != nil {
		tail = append(tail, orgEditView(w, s.edit))
	}
	if s.confirm != nil {
		tail = append(tail, orgConfirmView(w, s.confirm))
	}
	tail = append(tail, mutedStyle.Render(truncate(orgKeysHint, w-4)))

	// Measure the tail in rendered lines (the editor and confirmation cards
	// wrap to several), and count the blank line each section header adds, so
	// the body never overflows the screen.
	tailVis := 0
	for _, t := range tail {
		tailVis += lipgloss.Height(t)
	}
	avail := h - 2 - len(head) - tailVis - 3
	if avail < 3 {
		avail = 3
	}
	if len(mid) > avail {
		start := 0
		if curMid >= 0 {
			start = clamp(curMid-avail/2, 0, len(mid)-avail)
		}
		mid = mid[start : start+avail]
	}

	lines := make([]string, 0, len(head)+len(mid)+len(tail))
	lines = append(lines, head...)
	lines = append(lines, mid...)
	lines = append(lines, tail...)
	return strings.Join(lines, "\n")
}

// orgRoleLine renders one of the five roles: who fills it, what engine they
// run, whether they only read, and any save error pinned to this row. The
// width budgets keep mark+label+name+engine+mark+error under the frame's
// wrap width even in the worst case, so a long model name cannot shatter
// the layout.
func (s orgState) orgRoleLine(w int, i int, sel bool) string {
	mark, lab := orgRowMark(sel)
	err := s.rowError(orgRow{kind: rowRole, i: i})
	errB := 0
	if err != "" {
		errB = max(8, min(w/4, 30))
	}
	var b strings.Builder
	b.WriteString(mark + lab.Render(fmt.Sprintf("%-13s", roleKeys[i])))
	agent := s.roles[i]
	if agent == "" {
		hint := "←/→ pick an agent"
		if len(s.agents) == 0 {
			hint = "press a to add one"
		}
		b.WriteString(warnStyle.Render("— unassigned") + mutedStyle.Render(" "+hint))
	} else {
		b.WriteString(titleStyle.Render(truncate(agent, 20)))
		b.WriteString(mutedStyle.Render("  " + truncate(s.engineLine(agent), max(8, w-54-errB))))
		if readOnlyRoles[roleKeys[i]] || s.metaRO(agent) {
			b.WriteString(mutedStyle.Render(" (ro)"))
		}
	}
	if err != "" {
		b.WriteString(badStyle.Render("  ✖ " + truncate(err, errB)))
	}
	return b.String()
}

// orgSeatLine renders one panel seat: #n, the agent, their persona.
func (s orgState) orgSeatLine(w int, i int, sel bool) string {
	mark, lab := orgRowMark(sel)
	seat := s.panel[i]
	persona := strings.Join(strings.Fields(seat.Persona), " ")
	err := s.rowError(orgRow{kind: rowSeat, i: i})
	errB := 0
	if err != "" {
		errB = max(8, min(w/4, 30))
	}
	var b strings.Builder
	b.WriteString(mark + lab.Render(fmt.Sprintf("#%-2d ", i+1)) + titleStyle.Render(truncate(seat.Agent, 20)))
	if persona == "" {
		b.WriteString(warnStyle.Render("  — no persona, p"))
	} else {
		b.WriteString(mutedStyle.Render("  " + truncate(persona, max(8, w-44-errB))))
	}
	if s.metaRO(seat.Agent) || s.fillsReadOnlyRole(seat.Agent) {
		b.WriteString(mutedStyle.Render(" (ro)"))
	}
	if err != "" {
		b.WriteString(badStyle.Render("  ✖ " + truncate(err, errB)))
	}
	return b.String()
}

// orgPoolLine renders an agent holding no role and no seat. Its "value" is
// the department, which is what ←/→ steps through here.
func (s orgState) orgPoolLine(w int, i int, sel bool) string {
	pool := s.poolNames()
	if i >= len(pool) {
		return ""
	}
	name := pool[i]
	err := s.rowError(orgRow{kind: rowPool, i: i})
	errB := 0
	if err != "" {
		errB = max(8, min(w/4, 30))
	}
	mark, lab := orgRowMark(sel)
	var b strings.Builder
	b.WriteString(mark + lab.Render(truncate(name, 16)))
	b.WriteString(mutedStyle.Render("  " + truncate(s.engineLine(name), max(8, w-52-errB))))
	md := s.meta[name]
	if md.Dept == "" {
		b.WriteString(mutedStyle.Render(" (dept: ←/→)"))
	} else {
		b.WriteString(" " + pill(colorInfo, truncate(md.Dept, 12)))
	}
	if s.metaRO(name) {
		b.WriteString(mutedStyle.Render(" (ro)"))
	}
	if err != "" {
		b.WriteString(badStyle.Render("  ✖ " + truncate(err, errB)))
	}
	return b.String()
}

// orgDetail is the second line under the cursor row: a seat's full persona,
// or an agent's metadata and notes — the things too long for the row itself.
func (s orgState) orgDetail(w int, row orgRow) string {
	if row.kind == rowSeat && row.i < len(s.panel) {
		if p := strings.Join(strings.Fields(s.panel[row.i].Persona), " "); p != "" {
			return mutedStyle.Render(truncate("    "+p, max(10, w-6)))
		}
		return ""
	}
	name := s.rowAgent(row)
	if name == "" {
		return ""
	}
	md := s.meta[name]
	var bits []string
	if md.Title != "" {
		bits = append(bits, md.Title)
	}
	if md.Dept != "" {
		bits = append(bits, md.Dept)
	}
	if md.Tier != "" {
		bits = append(bits, md.Tier)
	}
	line := strings.Join(bits, " · ")
	if md.Notes != "" {
		if line != "" {
			line += " — "
		}
		line += md.Notes
	}
	if line == "" {
		return ""
	}
	return mutedStyle.Render(truncate("    "+line, max(10, w-6)))
}

// engineLine is how an agent reaches the world: OpenCode Go with its model
// (or the engine default), an OpenAI-compatible endpoint, or a plain command
// identified by its basename.
func (s orgState) engineLine(name string) string {
	a, ok := s.agents[name]
	if !ok {
		return "unknown agent"
	}
	switch a.Type {
	case "openai":
		model := a.Model
		if model == "" {
			model = "no model"
		}
		return "OpenAI-compat · " + model
	case "command":
		if a.Command == "" {
			return "command not set"
		}
		return filepath.Base(a.Command)
	default:
		if a.Model == "" {
			return "OpenCode Go · default"
		}
		return "OpenCode Go · " + a.Model
	}
}

// metaRO / fillsReadOnlyRole mark agents that only look: the sidecar flag, or
// a reviewer/interviewer role (that seat never edits files either).
func (s orgState) metaRO(name string) bool { return s.meta[name].ReadOnly }

func (s orgState) fillsReadOnlyRole(name string) bool {
	for i, r := range s.roles {
		if r == name && readOnlyRoles[roleKeys[i]] {
			return true
		}
	}
	return false
}

// rowError pins a config.Save field failure to the row it belongs to: exact
// match for roles and participant fields, name prefix for agents.*.
func (s orgState) rowError(row orgRow) string {
	name := s.rowAgent(row)
	for _, fe := range s.fieldErrs {
		f := fe.Field
		switch {
		case row.kind == rowRole && f == "roles."+roleKeys[row.i]:
			return fe.Msg
		case row.kind == rowSeat && orgParticipantField(f, row.i):
			return fe.Msg
		case name != "" && (f == "agents."+name || strings.HasPrefix(f, "agents."+name+".")):
			return fe.Msg
		}
	}
	return ""
}

// orgParticipantField matches roundtable.participants.N and any key under it
// (…agent, …persona) without confusing seat 1 with seat 10.
func orgParticipantField(f string, i int) bool {
	prefix := fmt.Sprintf("roundtable.participants.%d", i)
	return f == prefix || strings.HasPrefix(f, prefix+".")
}

// orgRowMark is the selection indicator: a prompt glyph and a highlighted
// label, without a background so the rest of the line keeps its colours.
func orgRowMark(sel bool) (string, lipgloss.Style) {
	if sel {
		return accentStyle.Render("❯ "), paletteSelStyle
	}
	return "  ", mutedStyle
}

// orgEditView draws the mini editor under the list: what is being edited,
// which field, and what has been committed so far.
func orgEditView(w int, e *orgEdit) string {
	title := "Edit " + e.agent
	switch e.kind {
	case editNew:
		title = "New agent"
	case editPersona:
		title = fmt.Sprintf("Seat #%d persona", e.seat+1)
	}
	rows := titleStyle.Render(title) +
		"\n" + accentStyle.Render(e.fields[e.sel].label) +
		mutedStyle.Render(fmt.Sprintf("  field %d/%d", e.sel+1, len(e.fields))) +
		"\n" + e.input.View()
	if e.sel > 0 {
		var done []string
		for i := 0; i < e.sel; i++ {
			v := e.vals[i]
			if v == "" {
				v = "—"
			}
			done = append(done, e.fields[i].label+"="+truncate(v, 18))
		}
		rows += "\n" + mutedStyle.Render(truncate(strings.Join(done, " · "), max(10, w-8)))
	}
	rows += "\n" + mutedStyle.Render("enter commit · esc cancel")
	return cardStyle.Width(min(max(20, w-6), 64)).Render(rows)
}

func orgConfirmView(w int, c *orgConfirm) string {
	style := cardStyle.BorderForeground(colorWarn)
	rows := warnStyle.Render("? "+c.prompt) + "\n" + mutedStyle.Render("y yes · n / esc no")
	return style.Width(min(max(20, w-6), 70)).Render(rows)
}

const orgKeysHint = "↑↓ row · ←→ value · ⏎ meta · p persona · + seat · x rm · [ ] order · a agent · d del · s save · esc back"

// orgIndexOf is index-of in a []string, -1 when absent.
func orgIndexOf(list []string, v string) int {
	for i, s := range list {
		if s == v {
			return i
		}
	}
	return -1
}
