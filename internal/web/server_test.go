package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/state"
)

const token = "test-token-0123456789"

// fakeAgent answers by stage (FACTORY_STAGE), like a real agent CLI would.
const fakeAgent = `#!/bin/sh
cat >/dev/null
case "$FACTORY_STAGE" in
interview) echo '{"questions":["Who is it for?"],"ready":true}' ;;
spec-draft|spec-revise|roundtable-spec-synthesis)
  printf '# Adder\n\n## Summary\nAdds numbers.\n\n` + "```json" + `\n%s\n` + "```" + `\n' '{"summary":"adds numbers","features":[{"id":"F1","title":"add","description":"d","acceptance":["2+3=5"]}],"use_cases":[{"id":"UC1","actor":"user","goal":"sum","scenario":"add 2 3","success":"5"}],"non_goals":[],"stack":"sh","test_command":"true","open_questions":[]}' ;;
doctor) echo OK ;;
*) echo "an opinion" ;;
esac
`

type env struct {
	t      *testing.T
	ws     string
	srv    *Server
	ts     *httptest.Server
	client *http.Client
	runs   []string
}

func setup(t *testing.T, mutate func(*Options)) *env {
	t.Helper()
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(agentPath, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "factory.json")
	cfg := `{"agents":{"a":{"type":"command","command":"` + agentPath + `"}},
	  "roles":{"interviewer":"a","planner":"a","builder":"a","reviewer":"a","moderator":"a"},
	  "roundtable":{"rounds":1,"participants":[{"agent":"a","persona":"engineer"}]}}`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	opt := Options{Workspace: filepath.Join(dir, "ws"), ConfigPath: cfgPath, Token: token}
	if mutate != nil {
		mutate(&opt)
	}
	srv, err := New(opt)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ws: opt.Workspace, srv: srv}
	srv.StartRun = func(pr *app.Project) (int, error) {
		if pr.P.Phase == state.PhaseSpec {
			return 0, errors.New("the spec has not been approved yet")
		}
		e.runs = append(e.runs, pr.P.Name)
		return 4242, nil
	}
	e.ts = httptest.NewServer(srv.Handler())
	jar, _ := cookiejar.New(nil)
	e.client = &http.Client{Jar: jar}
	t.Cleanup(func() { srv.Close(); e.ts.Close() })
	return e
}

func (e *env) do(method, path string, body any, headers ...string) (int, map[string]any) {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func (e *env) login() {
	e.t.Helper()
	if code, out := e.do("POST", "/api/login", map[string]string{"token": token}); code != 200 {
		e.t.Fatalf("login: %d %v", code, out)
	}
}

// waitChat polls until the chat is waiting for an answer (or inactive).
func (e *env) waitChat(id string, wantWaiting bool) map[string]any {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, out := e.do("GET", "/api/projects/"+id+"/chat", nil)
		if out["waiting"] == wantWaiting && (wantWaiting || out["active"] == false) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("chat never reached waiting=%v", wantWaiting)
	return nil
}

func TestAuth(t *testing.T) {
	e := setup(t, nil)
	if code, _ := e.do("GET", "/api/projects", nil); code != 401 {
		t.Fatalf("unauthenticated list: %d", code)
	}
	if code, out := e.do("GET", "/api/session", nil); code != 200 || out["auth_required"] != true || out["authenticated"] != false {
		t.Fatalf("session: %d %v", code, out)
	}
	if code, _ := e.do("POST", "/api/login", map[string]string{"token": "wrong"}); code != 401 {
		t.Fatalf("wrong token accepted: %d", code)
	}
	if code, _ := e.do("GET", "/api/projects", nil, "Authorization", "Bearer "+token); code != 200 {
		t.Fatalf("bearer: %d", code)
	}
	e.login()
	if code, _ := e.do("GET", "/api/projects", nil); code != 200 {
		t.Fatalf("cookie: %d", code)
	}
	// Cookie-authenticated writes from a foreign origin are refused.
	if code, _ := e.do("POST", "/api/projects", map[string]string{"name": "x", "idea": "y"}, "Origin", "https://evil.example"); code != 403 {
		t.Fatalf("cross-origin write: %d", code)
	}
	e.do("POST", "/api/logout", map[string]string{})
	if code, _ := e.do("GET", "/api/projects", nil); code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := setup(t, nil)
	for i := 0; i < 8; i++ {
		e.srv.fails["127.0.0.1"] = append(e.srv.fails["127.0.0.1"], time.Now())
	}
	if code, _ := e.do("POST", "/api/login", map[string]string{"token": token}); code != 429 {
		t.Fatalf("expected 429, got %d", code)
	}
}

func TestSecurityHeadersAndStatic(t *testing.T) {
	e := setup(t, func(o *Options) { o.FrameAncestors = "https://mysite.example" })
	res, err := http.Get(e.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `<script src="app.js"`) {
		t.Fatal("index not served")
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors https://mysite.example") || !strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("csp = %q", csp)
	}
	for _, f := range []string{"/app.js", "/style.css"} {
		if r, _ := http.Get(e.ts.URL + f); r.StatusCode != 200 {
			t.Fatalf("%s: %d", f, r.StatusCode)
		}
	}
}

func TestBasePathAndCORS(t *testing.T) {
	e := setup(t, func(o *Options) {
		o.BasePath = "/factory/"
		o.AllowOrigins = []string{"https://mysite.example"}
	})
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, _ := noRedirect.Get(e.ts.URL + "/factory")
	if res.StatusCode != 301 || res.Header.Get("Location") != "/factory/" {
		t.Fatalf("redirect: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if r, _ := http.Get(e.ts.URL + "/factory/app.js"); r.StatusCode != 200 {
		t.Fatalf("static under base path: %d", r.StatusCode)
	}
	if r, _ := http.Get(e.ts.URL + "/api/session"); r.StatusCode != 404 {
		t.Fatalf("outside base path should 404, got %d", r.StatusCode)
	}
	req, _ := http.NewRequest("OPTIONS", e.ts.URL+"/factory/api/projects", nil)
	req.Header.Set("Origin", "https://mysite.example")
	res, _ = http.DefaultClient.Do(req)
	if res.StatusCode != 204 || res.Header.Get("Access-Control-Allow-Origin") != "https://mysite.example" {
		t.Fatalf("preflight: %d %q", res.StatusCode, res.Header.Get("Access-Control-Allow-Origin"))
	}
	req.Header.Set("Origin", "https://other.example")
	res, _ = http.DefaultClient.Do(req)
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unknown origin got CORS headers")
	}
	// An allowed origin may use the API with a bearer token.
	code, _ := e.do("GET", "/factory/api/projects", nil, "Origin", "https://mysite.example", "Authorization", "Bearer "+token)
	if code != 200 {
		t.Fatalf("allowed origin: %d", code)
	}
}

func TestInterviewOverHTTP(t *testing.T) {
	e := setup(t, nil)
	e.login()

	if code, out := e.do("POST", "/api/projects", map[string]any{"name": "../evil", "idea": "x"}); code != 400 {
		t.Fatalf("bad name: %d %v", code, out)
	}
	if code, _ := e.do("POST", "/api/projects", map[string]any{"name": "adder", "idea": ""}); code != 400 {
		t.Fatal("empty idea accepted")
	}
	code, out := e.do("POST", "/api/projects", map[string]any{"name": "adder", "idea": "A CLI that adds two numbers", "auto_start": true})
	if code != 201 || out["id"] != "adder" {
		t.Fatalf("create: %d %v", code, out)
	}
	if code, _ := e.do("POST", "/api/projects", map[string]any{"name": "adder", "idea": "again"}); code != 409 {
		t.Fatal("duplicate accepted")
	}

	// Q1 from the interviewer.
	chat := e.waitChat("adder", true)
	msgs := chat["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if !strings.Contains(last["text"].(string), "Who is it for?") {
		t.Fatalf("first question: %v", last)
	}
	if code, _ := e.do("POST", "/api/projects/adder/chat", map[string]string{"text": "Accountants"}); code != 200 {
		t.Fatal("answer rejected")
	}
	// Approval question, with a quick "Approve" button.
	chat = e.waitChat("adder", true)
	msgs = chat["messages"].([]any)
	last = msgs[len(msgs)-1].(map[string]any)
	if !strings.Contains(last["text"].(string), "Approve the spec?") || last["quick"] == nil {
		t.Fatalf("approval prompt: %v", last)
	}
	if code, _ := e.do("GET", "/api/projects", nil); code != 200 {
		t.Fatal("list failed")
	}
	_, list := e.do("GET", "/api/projects", nil)
	item := list["projects"].([]any)[0].(map[string]any)
	if item["waiting"] != true || item["interview"] != true {
		t.Fatalf("list should show a waiting interview: %v", item)
	}
	if code, _ := e.do("POST", "/api/projects/adder/chat", map[string]string{"text": "y"}); code != 200 {
		t.Fatal("approval rejected")
	}
	e.waitChat("adder", false)

	_, detail := e.do("GET", "/api/projects/adder", nil)
	p := detail["project"].(map[string]any)
	if p["phase"] != state.PhaseSpecified {
		t.Fatalf("phase = %v", p["phase"])
	}
	if len(e.runs) != 1 || e.runs[0] != "adder" {
		t.Fatalf("auto start: %v", e.runs)
	}
	if code, _ := e.do("POST", "/api/projects/adder/chat", map[string]string{"text": "late"}); code != 409 {
		t.Fatal("answer accepted after interview ended")
	}

	// Reopening the spec requires an explicit reset.
	if code, _ := e.do("POST", "/api/projects/adder/spec", map[string]any{}); code != 409 {
		t.Fatal("reopen without reset accepted")
	}

	// Manual run + stop.
	if code, out := e.do("POST", "/api/projects/adder/run", map[string]any{}); code != 200 || out["pid"] != float64(4242) {
		t.Fatalf("run: %d %v", code, out)
	}
	if code, _ := e.do("POST", "/api/projects/adder/stop", map[string]any{}); code != 409 {
		t.Fatal("stop with nothing running should conflict")
	}

	// Artifacts: SPEC.md and the roundtable transcript are listed and readable.
	_, arts := e.do("GET", "/api/projects/adder/artifacts", nil)
	var paths []string
	for _, a := range arts["artifacts"].([]any) {
		paths = append(paths, a.(map[string]any)["path"].(string))
	}
	joined := strings.Join(paths, " ")
	if !strings.Contains(joined, "SPEC.md") || !strings.Contains(joined, ".factory/roundtables/001-spec.md") {
		t.Fatalf("artifacts: %v", paths)
	}
	code, art := e.do("GET", "/api/projects/adder/artifact?path=SPEC.md", nil)
	if code != 200 || !strings.HasPrefix(art["content"].(string), "# Adder") {
		t.Fatalf("SPEC.md: %d %v", code, art)
	}
	for _, bad := range []string{".factory/state.json", ".factory/config.json", "../../etc/passwd", ".factory/roundtables/../config.json"} {
		if code, _ := e.do("GET", "/api/projects/adder/artifact?path="+bad, nil); code != 404 {
			t.Fatalf("%s should not be readable: %d", bad, code)
		}
	}
}

func TestPauseAndResumeInterview(t *testing.T) {
	e := setup(t, nil)
	e.login()
	e.do("POST", "/api/projects", map[string]any{"name": "p1", "idea": "something"})
	e.waitChat("p1", true)
	if code, _ := e.do("POST", "/api/projects/p1/run", map[string]any{}); code != 409 {
		t.Fatal("run before spec approval accepted")
	}
	if code, _ := e.do("DELETE", "/api/projects/p1/spec", nil); code != 200 {
		t.Fatal("pause failed")
	}
	e.waitChat("p1", false)
	if code, out := e.do("POST", "/api/projects/p1/spec", map[string]any{}); code != 200 {
		t.Fatalf("resume: %d %v", code, out)
	}
	e.waitChat("p1", true)
}

func TestLogTail(t *testing.T) {
	e := setup(t, nil)
	e.login()
	e.do("POST", "/api/projects", map[string]any{"name": "logs", "idea": "x"})
	e.waitChat("logs", true)
	logPath := filepath.Join(e.ws, "logs", ".factory", "run.log")
	os.WriteFile(logPath, []byte("line one\n"), 0o644)
	_, out := e.do("GET", "/api/projects/logs/log?offset=-1", nil)
	if out["text"] != "line one\n" || out["offset"] != float64(9) {
		t.Fatalf("tail: %v", out)
	}
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("line two\n")
	f.Close()
	_, out = e.do("GET", "/api/projects/logs/log?offset=9", nil)
	if out["text"] != "line two\n" {
		t.Fatalf("incremental: %v", out)
	}
}

func TestDoctorAndConfig(t *testing.T) {
	e := setup(t, nil)
	e.login()
	_, cfg := e.do("GET", "/api/config", nil)
	if agents := cfg["agents"].([]any); len(agents) != 1 || agents[0].(map[string]any)["name"] != "a" {
		t.Fatalf("config: %v", cfg)
	}
	_, doc := e.do("POST", "/api/doctor", map[string]any{})
	res := doc["results"].([]any)[0].(map[string]any)
	if res["ok"] != true || res["reply"] != "OK" {
		t.Fatalf("doctor: %v", res)
	}
}

func putConfig(e *env, doc, meta any) (int, map[string]any) {
	return e.do("PUT", "/api/config", map[string]any{"config": doc, "meta": meta})
}

// A save round trip must not damage the config: placeholders stay symbolic,
// and a credential the browser was never shown is still on disk afterwards.
func TestConfigSaveRoundTrip(t *testing.T) {
	e := setup(t, nil)
	e.login()
	_, cfg := e.do("GET", "/api/config", nil)
	path := cfg["path"].(string)

	doc := map[string]any{
		// Editor bookkeeping the browser adds; it must be tolerated.
		"path": path, "editable": true, "notify": false,
		"agents": []any{map[string]any{
			"name": "a", "type": "command", "command": "python3",
			"args": []string{"{{config_dir}}/adapters/x.py"}, "timeout": "10m",
			"env":  map[string]any{"CAMEL_MODEL": "auto", "CAMEL_API_KEY": "sk-live-abc123"},
			"meta": map[string]any{"title": "ignored"},
		}},
		"roles":      cfg["roles"],
		"roundtable": cfg["roundtable"],
		"limits":     cfg["limits"],
	}
	meta := map[string]any{"a": map[string]any{"title": "Guest", "dept": "panel"}}
	if code, out := putConfig(e, doc, meta); code != 200 {
		t.Fatalf("save: %d %v", code, out)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "{{config_dir}}") {
		t.Fatalf("placeholder was frozen into an absolute path:\n%s", raw)
	}
	if !strings.Contains(string(raw), "sk-live-abc123") {
		t.Fatal("secret lost on save")
	}

	// The secret must come back withheld, not in the clear.
	_, cfg = e.do("GET", "/api/config", nil)
	ag := cfg["agents"].([]any)[0].(map[string]any)
	env := ag["env"].(map[string]any)
	if env["CAMEL_API_KEY"] != nil {
		t.Fatalf("secret exposed to the browser: %v", env["CAMEL_API_KEY"])
	}
	if env["CAMEL_MODEL"] != "auto" {
		t.Fatalf("plain value lost: %v", env["CAMEL_MODEL"])
	}
	secrets := ag["env_secret"].([]any)
	if len(secrets) != 1 || secrets[0] != "CAMEL_API_KEY" {
		t.Fatalf("env_secret = %v", secrets)
	}
	if got := ag["args"].([]any)[0]; got != "{{config_dir}}/adapters/x.py" {
		t.Fatalf("args = %v", got)
	}
	if title := ag["meta"].(map[string]any)["title"]; title != "Guest" {
		t.Fatalf("meta = %v", ag["meta"])
	}

	// Saving what the browser received (with the nil secret) must inherit it.
	if code, out := putConfig(e, map[string]any{
		"agents": cfg["agents"], "roles": cfg["roles"],
		"roundtable": cfg["roundtable"], "limits": cfg["limits"],
	}, map[string]any{"a": map[string]any{"title": "Guest", "dept": "panel"}}); code != 200 {
		t.Fatalf("save round 2: %d %v", code, out)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "sk-live-abc123") {
		t.Fatalf("withheld secret was blanked on re-save:\n%s", raw)
	}
	if strings.Contains(string(raw), "null") {
		t.Fatalf("nil secret written to disk as null:\n%s", raw)
	}

	metaBytes, err := os.ReadFile(filepath.Join(filepath.Dir(path), "org.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(metaBytes), `"Guest"`) {
		t.Fatalf("org metadata not written: %s", metaBytes)
	}
}

// A rejected save must report the field at fault and leave the file untouched.
func TestConfigSaveRejectsInvalid(t *testing.T) {
	e := setup(t, nil)
	e.login()
	_, cfg := e.do("GET", "/api/config", nil)
	path := cfg["path"].(string)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	roles := cfg["roles"].(map[string]any)
	roles["reviewer"] = "ghost"
	code, out := putConfig(e, map[string]any{
		"agents": cfg["agents"], "roles": roles,
		"roundtable": cfg["roundtable"], "limits": cfg["limits"],
	}, map[string]any{})
	if code != 400 {
		t.Fatalf("status = %d, want 400: %v", code, out)
	}
	fields := out["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("fields = %v", fields)
	}
	if f := fields[0].(map[string]any)["field"]; f != "roles.reviewer" {
		t.Fatalf("field = %v, want roles.reviewer", f)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a rejected save modified the config")
	}
}

// Unknown keys must still be rejected — only the editor's own bookkeeping is
// stripped before validation.
func TestConfigSaveStillCatchesTypos(t *testing.T) {
	e := setup(t, nil)
	e.login()
	_, cfg := e.do("GET", "/api/config", nil)
	agents := cfg["agents"].([]any)
	agents[0].(map[string]any)["modle"] = "typo"
	code, out := putConfig(e, map[string]any{
		"agents": agents, "roles": cfg["roles"],
		"roundtable": cfg["roundtable"], "limits": cfg["limits"],
	}, map[string]any{})
	if code != 400 {
		t.Fatalf("status = %d, want 400: %v", code, out)
	}
	fields := out["fields"].([]any)
	if len(fields) != 1 || fields[0].(map[string]any)["field"] != "modle" {
		t.Fatalf("fields = %v", fields)
	}
}

func TestConfigSaveRequiresAuth(t *testing.T) {
	e := setup(t, nil) // never logged in
	code, _ := e.do("PUT", "/api/config", map[string]any{"config": map[string]any{}})
	if code != 401 {
		t.Fatalf("status = %d, want 401", code)
	}
}
