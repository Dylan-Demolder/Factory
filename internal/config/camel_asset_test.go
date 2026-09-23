package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The CAMEL bridge is embedded and then installed to
// ~/.config/factory/adapters/, where a syntax error would only surface the
// first time a build runs — inside a detached process, as a failed task.
// Compiling it here turns that into a test failure.
func TestEmbeddedCamelAdapterCompiles(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	path := filepath.Join(t.TempDir(), "camel_agent.py")
	if err := os.WriteFile(path, []byte(CamelAdapter), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(py, "-m", "py_compile", path).CombinedOutput(); err != nil {
		t.Fatalf("the embedded camel adapter does not compile: %v\n%s", err, out)
	}
}

// A camel agent is only useful as a builder if it can touch files, and only
// safe as a reviewer if it cannot. Both halves are decided by these two
// tool sets and the flag factory passes, so pin them.
func TestEmbeddedCamelAdapterHasBothToolModes(t *testing.T) {
	required := map[string]string{
		"FACTORY_READONLY":                   "factory's read-only flag",
		"READ_TOOLS":                         "the tools reviewers get",
		"WRITE_TOOLS":                        "the tools builders get",
		"replace_in_file":                    "an exact-match edit tool",
		"path escapes the project directory": "the path sandbox",
	}
	for needle, what := range required {
		if !strings.Contains(CamelAdapter, needle) {
			t.Errorf("the embedded adapter is missing %s (looked for %q)", what, needle)
		}
	}
	// The read-only set must never contain a writer — that is the whole
	// safety property for reviewers and roundtable seats.
	if i := strings.Index(CamelAdapter, "READ_TOOLS ="); i >= 0 {
		seg := CamelAdapter[i:]
		if j := strings.Index(seg, "WRITE_TOOLS ="); j >= 0 {
			seg = seg[:j]
		}
		for _, fn := range []string{"write_file", "replace_in_file"} {
			if strings.Contains(seg, fn+"(") {
				t.Errorf("READ_TOOLS appears to include %s — reviewers could modify the project", fn)
			}
		}
	}
}

// The bridge must keep reporting failures as single lines: factory captures
// stdout/stderr and shows the tail, where a traceback is noise.
func TestEmbeddedCamelAdapterAvoidsTracebacks(t *testing.T) {
	if strings.Count(CamelAdapter, "except Exception") < 2 {
		t.Error("expected broad catches around tool and model calls so failures stay one line")
	}
}

// A builder that cannot set an executable bit cannot satisfy a criterion
// asking for one: write_file always creates 0644, which is how wordcnt's
// reviewer rejected a task four times. Builders must get that tool and
// reviewers must not.
func TestEmbeddedCamelAdapterCanMarkExecutable(t *testing.T) {
	if !strings.Contains(CamelAdapter, "make_executable") {
		t.Fatal("the adapter has no way to set an executable bit")
	}
	i := strings.Index(CamelAdapter, "WRITE_TOOLS =")
	if i < 0 {
		t.Fatal("no WRITE_TOOLS list found")
	}
	if !strings.Contains(CamelAdapter[i:], "make_executable") {
		t.Error("make_executable is missing from WRITE_TOOLS — builders cannot chmod")
	}
	// The read-only set must not include it: reviewers are read-only by
	// construction, so check the READ_TOOLS declaration specifically rather
	// than the text before WRITE_TOOLS (which contains the function itself).
	r := strings.Index(CamelAdapter, "READ_TOOLS =")
	if r < 0 {
		t.Fatal("no READ_TOOLS list found")
	}
	if strings.Contains(CamelAdapter[r:i], "make_executable") {
		t.Error("make_executable leaked into READ_TOOLS — reviewers could chmod files")
	}
}

// Briefs ask for deletions — "fold run_t3_test.go into run_test.go and
// delete that task-scoped file" was wordcnt's T4, rejected four times with
// no way to comply.
func TestEmbeddedCamelAdapterCanDeleteFiles(t *testing.T) {
	for _, needle := range []string{"def delete_file", ".factory/ — factory owns it"} {
		if !strings.Contains(CamelAdapter, needle) {
			t.Errorf("adapter missing %q", needle)
		}
	}
	i := strings.Index(CamelAdapter, "WRITE_TOOLS =")
	if i < 0 || !strings.Contains(CamelAdapter[i:], "delete_file") {
		t.Error("delete_file is missing from WRITE_TOOLS — builders cannot delete")
	}
	r := strings.Index(CamelAdapter, "READ_TOOLS =")
	if r >= 0 && strings.Contains(CamelAdapter[r:i], "delete_file") {
		t.Error("delete_file leaked into READ_TOOLS — reviewers could delete files")
	}
}

// The build prompt tells builders to run the suite themselves, and the
// acceptance trial has them use the software hands-on. Without a command
// tool a camel builder could do neither — wordcnt's round-3 trial came back
// "No command executed" for all nine use cases.
func TestEmbeddedCamelAdapterCanRunCommands(t *testing.T) {
	for _, needle := range []string{"def run_command", "subprocess.run"} {
		if !strings.Contains(CamelAdapter, needle) {
			t.Errorf("adapter missing %q", needle)
		}
	}
	i := strings.Index(CamelAdapter, "WRITE_TOOLS =")
	if i < 0 || !strings.Contains(CamelAdapter[i:i+200], "run_command") {
		t.Error("run_command missing from WRITE_TOOLS — builders cannot verify their own work")
	}
	r := strings.Index(CamelAdapter, "READ_TOOLS =")
	if r >= 0 && strings.Contains(CamelAdapter[r:i], "run_command") {
		t.Error("run_command leaked into READ_TOOLS — reviewers could execute commands")
	}
	// A hard timeout must be part of it: a hung command cannot wedge a build.
	if !strings.Contains(CamelAdapter, "command timed out") {
		t.Error("run_command has no timeout handling")
	}
}
