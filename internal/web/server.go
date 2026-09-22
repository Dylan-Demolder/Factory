// Package web serves factory's browser interface and JSON API: create
// projects, run the spec interview as a chat, start/stop builds, and watch
// progress, logs and every roundtable transcript — locally or remotely.
package web

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dylan-demolder/factory/internal/agent"
	"github.com/dylan-demolder/factory/internal/app"
	"github.com/dylan-demolder/factory/internal/config"
	"github.com/dylan-demolder/factory/internal/state"
)

//go:embed static
var staticFS embed.FS

const cookieName = "factory_session"

// Options configures the server.
type Options struct {
	Workspace      string   // directory holding projects
	ConfigPath     string   // explicit config for new projects ("" = normal resolution)
	Token          string   // required access token ("" disables auth — loopback only)
	BasePath       string   // URL prefix when mounted behind a proxy, e.g. "/factory"
	AllowOrigins   []string // exact origins allowed to call the API cross-site (no wildcards)
	FrameAncestors string   // CSP frame-ancestors value
	Exe            string   // factory binary used for background runs ("" = self)
	Version        string
}

type session struct {
	chat      *Chat
	cancel    context.CancelFunc
	active    bool
	autoStart bool
	err       string
	started   time.Time
}

// Server is the web interface.
type Server struct {
	opt      Options
	mu       sync.Mutex
	sessions map[string]*session
	fails    map[string][]time.Time

	// StartRun launches a background build; replaceable in tests.
	StartRun func(pr *app.Project) (int, error)
}

func New(opt Options) (*Server, error) {
	if opt.Workspace == "" {
		return nil, errors.New("workspace is required")
	}
	abs, err := filepath.Abs(opt.Workspace)
	if err != nil {
		return nil, err
	}
	opt.Workspace = abs
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	opt.BasePath = "/" + strings.Trim(opt.BasePath, "/")
	if opt.BasePath == "/" {
		opt.BasePath = ""
	}
	if opt.FrameAncestors == "" {
		opt.FrameAncestors = "'self'"
	}
	s := &Server{opt: opt, sessions: map[string]*session{}, fails: map[string][]time.Time{}}
	s.StartRun = func(pr *app.Project) (int, error) { return pr.StartRun(s.opt.Exe) }
	return s, nil
}

// Close stops any running interviews.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.cancel != nil {
			sess.cancel()
		}
	}
}

// Handler returns the full HTTP handler, including the base path.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.FileServer(http.FS(static))
	mux.Handle("GET /", files)

	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	api := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireAuth(h)) }
	api("GET /api/projects", s.handleList)
	api("POST /api/projects", s.handleCreate)
	api("GET /api/projects/{id}", s.handleProject)
	api("POST /api/projects/{id}/spec", s.handleSpecStart)
	api("DELETE /api/projects/{id}/spec", s.handleSpecStop)
	api("GET /api/projects/{id}/chat", s.handleChat)
	api("POST /api/projects/{id}/chat", s.handleAnswer)
	api("POST /api/projects/{id}/run", s.handleRun)
	api("POST /api/projects/{id}/stop", s.handleStop)
	api("GET /api/projects/{id}/log", s.handleLog)
	api("GET /api/projects/{id}/artifacts", s.handleArtifacts)
	api("GET /api/projects/{id}/artifact", s.handleArtifact)
	api("GET /api/config", s.handleConfig)
	api("POST /api/doctor", s.handleDoctor)
	notFound := s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint")
	})
	for _, m := range []string{"GET", "POST", "DELETE"} {
		mux.Handle(m+" /api/", notFound)
	}

	var h http.Handler = mux
	if s.opt.BasePath != "" {
		inner := http.StripPrefix(s.opt.BasePath, mux)
		base := s.opt.BasePath
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == base {
				http.Redirect(w, r, base+"/", http.StatusMovedPermanently)
				return
			}
			if !strings.HasPrefix(r.URL.Path, base+"/") {
				http.NotFound(w, r)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}
	return s.headers(s.cors(h))
}

// ---- middleware ----

func (s *Server) headers(next http.Handler) http.Handler {
	csp := "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors " + s.opt.FrameAncestors
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if strings.Contains(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin string) bool {
	for _, o := range s.opt.AllowOrigins {
		if strings.EqualFold(strings.TrimRight(o, "/"), origin) {
			return true
		}
	}
	return false
}

func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
			h.Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) sessionValue() string {
	m := hmac.New(sha256.New, []byte(s.opt.Token))
	m.Write([]byte("factory-session-v1"))
	return hex.EncodeToString(m.Sum(nil))
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// authKind reports how the request authenticated: "bearer", "cookie", "open" or "".
func (s *Server) authKind(r *http.Request) string {
	if s.opt.Token == "" {
		return "open"
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") && equal(strings.TrimPrefix(h, "Bearer "), s.opt.Token) {
		return "bearer"
	}
	if c, err := r.Cookie(cookieName); err == nil && equal(c.Value, s.sessionValue()) {
		return "cookie"
	}
	return ""
}

func requestHost(r *http.Request) string {
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		return strings.TrimSpace(strings.Split(h, ",")[0])
	}
	return r.Host
}

// sameOrigin guards cookie-authenticated writes against cross-site requests.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // same-origin navigations and non-browser clients
	}
	if s.originAllowed(origin) {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, requestHost(r))
}

func (s *Server) requireAuth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := s.authKind(r)
		if kind == "" {
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && kind != "bearer" && !s.sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "cross-origin request refused; add this origin with --allow-origin")
			return
		}
		h(w, r)
	})
}

// ---- auth endpoints ----

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"auth_required": s.opt.Token != "",
		"authenticated": s.authKind(r) != "",
		"version":       s.opt.Version,
		"workspace":     s.opt.Workspace,
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross-origin login refused")
		return
	}
	ip := clientIP(r)
	s.mu.Lock()
	var recent []time.Time
	for _, t := range s.fails[ip] {
		if time.Since(t) < 10*time.Minute {
			recent = append(recent, t)
		}
	}
	s.fails[ip] = recent
	s.mu.Unlock()
	if len(recent) >= 8 {
		writeErr(w, http.StatusTooManyRequests, "too many failed logins; try again later")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.opt.Token != "" && !equal(strings.TrimSpace(body.Token), s.opt.Token) {
		s.mu.Lock()
		s.fails[ip] = append(s.fails[ip], time.Now())
		s.mu.Unlock()
		time.Sleep(500 * time.Millisecond)
		writeErr(w, http.StatusUnauthorized, "wrong token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: s.sessionValue(), Path: s.opt.BasePath + "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 24 * 3600,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: s.opt.BasePath + "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- projects ----

func (s *Server) projectDir(id string) (string, error) {
	if err := app.ValidName(id); err != nil {
		return "", err
	}
	return filepath.Join(s.opt.Workspace, id), nil
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) (string, *app.Project, bool) {
	id := r.PathValue("id")
	dir, err := s.projectDir(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return "", nil, false
	}
	pr, err := app.Open(dir, "")
	if err != nil {
		status := http.StatusInternalServerError
		if !(&state.Store{Root: dir}).Exists() {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return "", nil, false
	}
	return id, pr, true
}

func (s *Server) sessionInfo(id string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return map[string]any{"exists": false, "active": false}
	}
	_, waiting := sess.chat.Since(1 << 30)
	return map[string]any{"exists": true, "active": sess.active, "waiting": waiting, "error": sess.err, "auto_start": sess.autoStart, "started": sess.started}
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := app.List(s.opt.Workspace)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	type item struct {
		app.Summary
		Interview bool `json:"interview"`
		Waiting   bool `json:"waiting"`
	}
	out := make([]item, 0, len(list))
	for _, p := range list {
		info := s.sessionInfo(p.ID)
		out = append(out, item{Summary: p, Interview: info["active"] == true, Waiting: info["waiting"] == true})
	}
	writeJSON(w, map[string]any{"projects": out, "workspace": s.opt.Workspace})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Idea      string `json:"idea"`
		AutoStart bool   `json:"auto_start"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	dir, err := s.projectDir(body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(body.Idea) == "" {
		writeErr(w, http.StatusBadRequest, "describe the idea")
		return
	}
	if (&state.Store{Root: dir}).Exists() {
		writeErr(w, http.StatusConflict, "a project with that name already exists")
		return
	}
	cfg, cfgPath, err := config.Resolve(s.opt.ConfigPath, "")
	if err != nil {
		writeErr(w, http.StatusFailedDependency, "no usable factory config: "+err.Error())
		return
	}
	pr, err := app.Create(dir, body.Name, body.Idea, cfg, cfgPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.startSpec(body.Name, pr, body.AutoStart); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"id": body.Name})
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	id, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	sum, _ := app.Summarize(id, pr.Store.Root)
	writeJSON(w, map[string]any{
		"summary": sum,
		"project": pr.P,
		"session": s.sessionInfo(id),
	})
}

// ---- spec interview ----

func (s *Server) startSpec(id string, pr *app.Project, autoStart bool) error {
	s.mu.Lock()
	if old, ok := s.sessions[id]; ok && old.active {
		s.mu.Unlock()
		return errors.New("an interview is already in progress")
	}
	ctx, cancel := context.WithCancel(context.Background())
	sess := &session{chat: NewChat(), cancel: cancel, active: true, autoStart: autoStart, started: time.Now().UTC()}
	s.sessions[id] = sess
	s.mu.Unlock()

	e, err := pr.Engine(sess.chat)
	if err != nil {
		cancel()
		s.mu.Lock()
		sess.active, sess.err = false, err.Error()
		s.mu.Unlock()
		return err
	}
	e.UI = sess.chat
	sess.chat.System("Interview started. Answers are saved as you go — you can close this page and come back.")

	go func() {
		defer cancel()
		err := e.Spec(ctx)
		msg := ""
		switch {
		case err != nil && ctx.Err() != nil:
			sess.chat.System("Interview paused. Your answers are saved; resume any time.")
		case err != nil:
			msg = err.Error()
			sess.chat.Error("The interview stopped: " + msg)
		default:
			sess.chat.System("✔ Spec approved and committed as SPEC.md.")
			if sess.autoStart {
				if pid, err := s.StartRun(pr); err != nil {
					sess.chat.Error("Could not start the build: " + err.Error())
				} else {
					sess.chat.System(fmt.Sprintf("▶ Autonomous build started in the background (pid %d). Watch it on the Overview and Live log tabs.", pid))
				}
			} else {
				sess.chat.System("Start the build whenever you're ready.")
			}
		}
		sess.chat.Close()
		s.mu.Lock()
		sess.active, sess.err = false, msg
		s.mu.Unlock()
	}()
	return nil
}

func (s *Server) handleSpecStart(w http.ResponseWriter, r *http.Request) {
	id, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	var body struct {
		Reset     bool `json:"reset"`
		AutoStart bool `json:"auto_start"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &body); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if pid, running := pr.Running(); running {
		writeErr(w, http.StatusConflict, fmt.Sprintf("a build is running (pid %d); stop it first", pid))
		return
	}
	if pr.P.Phase != state.PhaseSpec {
		if !body.Reset {
			writeErr(w, http.StatusConflict, "the spec is already approved; reopening it resets the plan and task progress (send reset: true)")
			return
		}
		if err := pr.ResetForSpec(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if err := s.startSpec(id, pr, body.AutoStart); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleSpecStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	sess, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok || !sess.active {
		writeErr(w, http.StatusConflict, "no interview in progress")
		return
	}
	sess.cancel()
	sess.chat.Close()
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	after, _ := strconv.Atoi(r.URL.Query().Get("after"))
	s.mu.Lock()
	sess, ok := s.sessions[id]
	var active bool
	var errMsg string
	if ok {
		active, errMsg = sess.active, sess.err
	}
	s.mu.Unlock()
	if !ok {
		writeJSON(w, map[string]any{"messages": []Msg{}, "waiting": false, "active": false, "exists": false})
		return
	}
	msgs, waiting := sess.chat.Since(after)
	writeJSON(w, map[string]any{"messages": msgs, "waiting": waiting, "active": active, "exists": true, "error": errMsg})
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.mu.Lock()
	sess, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		writeErr(w, http.StatusConflict, "no interview in progress")
		return
	}
	if err := sess.chat.Answer(body.Text); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// ---- runs ----

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	_, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	pid, err := s.StartRun(pr)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]int{"pid": pid})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	_, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	if err := pr.Stop(); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

const (
	logTail  = 64 * 1024
	logChunk = 256 * 1024
)

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	_, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil {
		offset = -1
	}
	f, err := os.Open(pr.Store.Path("run.log"))
	if err != nil {
		writeJSON(w, map[string]any{"offset": 0, "size": 0, "text": ""})
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	size := fi.Size()
	if offset < 0 {
		offset = max(0, size-logTail)
	}
	if offset > size {
		offset = 0 // log was truncated
	}
	buf := make([]byte, min(logChunk, size-offset))
	n, _ := f.ReadAt(buf, offset)
	writeJSON(w, map[string]any{"offset": offset + int64(n), "size": size, "text": string(buf[:n])})
}

// ---- artifacts ----

// Artifact is a readable file produced during a build.
type Artifact struct {
	Path  string    `json:"path"`
	Group string    `json:"group"`
	Size  int64     `json:"size"`
	Mod   time.Time `json:"modified"`
}

func listArtifacts(root string) []Artifact {
	var out []Artifact
	add := func(rel, group string) {
		if fi, err := os.Stat(filepath.Join(root, rel)); err == nil && fi.Mode().IsRegular() {
			out = append(out, Artifact{Path: filepath.ToSlash(rel), Group: group, Size: fi.Size(), Mod: fi.ModTime().UTC()})
		}
	}
	for _, f := range []string{"SPEC.md", "REPORT.md", "README.md"} {
		add(f, "project")
	}
	for _, f := range []string{".factory/spec.json", ".factory/plan.json"} {
		add(f, "plan")
	}
	for _, dir := range []string{"roundtables", "tasks", "acceptance"} {
		entries, _ := os.ReadDir(filepath.Join(root, ".factory", dir))
		for _, e := range entries {
			if !e.IsDir() && !strings.HasSuffix(e.Name(), ".tmp") {
				add(filepath.Join(".factory", dir, e.Name()), dir)
			}
		}
	}
	return out
}

func (s *Server) handleArtifacts(w http.ResponseWriter, r *http.Request) {
	_, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	writeJSON(w, map[string]any{"artifacts": listArtifacts(pr.Store.Root)})
}

const artifactLimit = 2 << 20

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	_, pr, ok := s.open(w, r)
	if !ok {
		return
	}
	want := r.URL.Query().Get("path")
	// Only files that appear in the listing can be read.
	for _, a := range listArtifacts(pr.Store.Root) {
		if a.Path != want {
			continue
		}
		f, err := os.Open(filepath.Join(pr.Store.Root, filepath.FromSlash(a.Path)))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(io.LimitReader(f, artifactLimit+1))
		truncated := len(data) > artifactLimit
		if truncated {
			data = data[:artifactLimit]
		}
		writeJSON(w, map[string]any{"path": a.Path, "content": string(data), "truncated": truncated})
		return
	}
	writeErr(w, http.StatusNotFound, "no such artifact")
}

// ---- config & doctor ----

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg, path, err := config.Resolve(s.opt.ConfigPath, "")
	if err != nil {
		writeJSON(w, map[string]any{"error": err.Error()})
		return
	}
	type agentInfo struct {
		Name    string `json:"name"`
		Type    string `json:"type"`
		Model   string `json:"model,omitempty"`
		Command string `json:"command,omitempty"`
		Timeout string `json:"timeout,omitempty"`
	}
	var agents []agentInfo
	for name, a := range cfg.Agents {
		info := agentInfo{Name: name, Type: a.Type, Model: a.Model, Command: filepath.Base(a.Command)}
		if a.Timeout.Duration > 0 {
			info.Timeout = a.Timeout.String()
		}
		if a.Type == "openai" {
			info.Command = ""
		}
		agents = append(agents, info)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].Name < agents[j].Name })
	writeJSON(w, map[string]any{
		"path":       path,
		"agents":     agents,
		"roles":      cfg.Roles,
		"roundtable": cfg.Roundtable,
		"limits":     cfg.Limits,
		"notify":     cfg.NotifyCommand != "",
	})
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	cfg, _, err := config.Resolve(s.opt.ConfigPath, "")
	if err != nil {
		writeErr(w, http.StatusFailedDependency, err.Error())
		return
	}
	agents, err := agent.NewAll(cfg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dir, _ := os.MkdirTemp("", "factory-doctor-")
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	type result struct {
		Name     string `json:"name"`
		OK       bool   `json:"ok"`
		Reply    string `json:"reply,omitempty"`
		Error    string `json:"error,omitempty"`
		Duration string `json:"duration"`
	}
	results := make([]result, 0, len(agents))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, a := range agents {
		wg.Add(1)
		go func(name string, a agent.Agent) {
			defer wg.Done()
			start := time.Now()
			out, err := a.Run(ctx, agent.Request{Stage: "doctor", Prompt: "Reply with exactly the word OK and nothing else.", Dir: dir, ReadOnly: true})
			res := result{Name: name, OK: err == nil, Reply: agent.Tail(out, 120), Duration: time.Since(start).Round(100 * time.Millisecond).String()}
			if err != nil {
				res.Error = err.Error()
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
		}(name, a)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	writeJSON(w, map[string]any{"results": results})
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}
