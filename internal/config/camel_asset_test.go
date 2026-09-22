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
