package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newProjectDir writes the marker Delete looks for: .factory/state.json.
func newProjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".factory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".factory", "state.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDeleteRemovesAProject(t *testing.T) {
	dir := newProjectDir(t)
	if err := Delete(dir); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still present after delete: %v", err)
	}
}

// A directory that merely exists must not be removable through this API —
// otherwise a workspace typo could erase unrelated work.
func TestDeleteRefusesADirectoryThatIsNotAProject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Delete(dir)
	if err == nil || !strings.Contains(err.Error(), "not a factory project") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("a non-project was deleted anyway: %v", statErr)
	}
}

// Deleting out from under a live `factory run` would leave it writing into a
// hole, so a running build stops this call — and nothing is signalled here.
func TestDeleteRefusesARunningBuild(t *testing.T) {
	dir := newProjectDir(t)
	// Our own pid is guaranteed alive; Delete must refuse before it would
	// ever try to terminate anything.
	if err := os.WriteFile(filepath.Join(dir, ".factory", "run.pid"),
		[]byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Delete(dir)
	if err == nil || !strings.Contains(err.Error(), "stop it first") {
		t.Fatalf("err = %v, want 'stop it first'", err)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Errorf("project deleted while its build was running: %v", statErr)
	}
}

// The root and a bare separator are never legitimate targets; the guard must
// fire before any existence check, so the refusal cannot depend on what is
// installed at "/".
func TestDeleteRefusesTheFilesystemRoot(t *testing.T) {
	for _, p := range []string{"/", string(filepath.Separator)} {
		err := Delete(p)
		if err == nil || !strings.Contains(err.Error(), "not inside a workspace") {
			t.Errorf("Delete(%q) = %v, want a refusal", p, err)
		}
	}
}

// A stale pid file (process long gone) must not block a delete — otherwise a
// crashed build would make a project permanently undeletable.
func TestDeleteAllowsAStalePidFile(t *testing.T) {
	dir := newProjectDir(t)
	if err := os.WriteFile(filepath.Join(dir, ".factory", "run.pid"),
		[]byte("999999"), 0o644); err != nil { // not a live process
		t.Fatal(err)
	}
	if err := Delete(dir); err != nil {
		t.Fatalf("stale pid blocked the delete: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("directory still present: %v", err)
	}
}
