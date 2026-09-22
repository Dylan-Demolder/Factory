package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDeleteProjectEndToEnd(t *testing.T) {
	e := setup(t, nil)
	e.login()
	dir := filepath.Join(e.ws, "gone")

	code, out := e.do("POST", "/api/projects", map[string]any{"name": "gone", "idea": "a project to delete"})
	if code != 200 && code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("project not created: %v", err)
	}

	// An interview session is live for this project; delete must cancel it
	// rather than leave a goroutine asking a deleted directory questions.
	code, out = e.do("DELETE", "/api/projects/gone", nil)
	if code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}

	// Deleting twice is a 404, not a crash.
	if code, _ := e.do("DELETE", "/api/projects/gone", nil); code != 404 {
		t.Errorf("second delete = %d, want 404", code)
	}
}

// A running build must block deletion unless the caller explicitly asks for
// both steps; force stops the build and then removes the project.
func TestDeleteRefusesRunningBuildUnlessForced(t *testing.T) {
	e := setup(t, nil)
	e.login()
	dir := filepath.Join(e.ws, "busy")

	if code, out := e.do("POST", "/api/projects", map[string]any{"name": "busy", "idea": "busy project"}); code != 200 && code != 201 {
		t.Fatalf("create: %d %v", code, out)
	}
	// A child we can safely terminate: `sleep`. Reap it in the background —
	// without that it becomes a zombie after SIGTERM, and kill(pid, 0)
	// succeeds on a zombie, so "still running" would never clear. Real
	// builds are reparented to init and reaped the same way.
	child := exec.Command("sleep", "300")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	reaped := make(chan struct{})
	go func() { _ = child.Wait(); close(reaped) }()
	defer func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
		select {
		case <-reaped:
		case <-time.After(3 * time.Second):
		}
	}()
	pid := child.Process.Pid
	if err := os.WriteFile(filepath.Join(dir, ".factory", "run.pid"),
		[]byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Without force: refuse, and leave everything in place.
	code, out := e.do("DELETE", "/api/projects/busy", nil)
	if code != 409 {
		t.Fatalf("delete of a running build = %d %v, want 409", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "force") {
		t.Errorf("error = %q, should tell the caller about force", msg)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("project deleted despite a running build: %v", err)
	}

	// With force: stop, then delete.
	code, out = e.do("DELETE", "/api/projects/busy?force=true", nil)
	if code != 200 {
		t.Fatalf("forced delete: %d %v", code, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory still exists after force: %v", err)
	}
	// The build must actually be gone, not orphaned and still writing.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := child.Process.Signal(syscall.Signal(0)); err != nil {
			break // reaped: terminated
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err == nil {
		t.Error("the build process survived the forced delete")
	}
}

// Path safety: the id is validated before anything is resolved, so a crafted
// name never reaches RemoveAll.
func TestDeleteRejectsAnInvalidName(t *testing.T) {
	e := setup(t, nil)
	e.login()
	// Names app.ValidName rejects, chosen so they are URL-safe: a "../" probe
	// would be normalised by the HTTP client instead of reaching the handler.
	for _, id := range []string{"-bad", ".hidden"} {
		code, out := e.do("DELETE", "/api/projects/"+id, nil)
		if code != 400 {
			t.Errorf("DELETE %q = %d %v, want 400", id, code, out)
		}
	}
}
