package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadEnvFileSetsMissingVariables(t *testing.T) {
	path := writeEnv(t, `
# a comment
CAMEL_API_KEY=qaml_live_abc123
FACTORY_TOKEN="a token with spaces"
export CAMEL_MODEL=auto

not an assignment
1BAD=nope
BAD-KEY=nope
EMPTY=
`)
	t.Setenv("CAMEL_API_KEY", "")
	os.Unsetenv("CAMEL_API_KEY")

	applied := LoadEnvFile(path)
	got := map[string]bool{}
	for _, k := range applied {
		got[k] = true
	}
	for _, want := range []string{"CAMEL_API_KEY", "FACTORY_TOKEN", "CAMEL_MODEL"} {
		if !got[want] {
			t.Errorf("%s not applied; applied = %v", want, applied)
		}
	}
	if v := os.Getenv("CAMEL_API_KEY"); v != "qaml_live_abc123" {
		t.Errorf("CAMEL_API_KEY = %q", v)
	}
	if v := os.Getenv("FACTORY_TOKEN"); v != "a token with spaces" {
		t.Errorf("quotes not stripped: %q", v)
	}
	// Malformed and empty lines must never become variables.
	for _, bad := range []string{"1BAD", "BAD-KEY", "EMPTY"} {
		if v, ok := os.LookupEnv(bad); ok && v != "" {
			t.Errorf("%s should not have been set, got %q", bad, v)
		}
	}
}

// An explicit export in the shell must win over the file — that is how a user
// points factory at a different key for one run.
func TestLoadEnvFileDoesNotOverrideExisting(t *testing.T) {
	path := writeEnv(t, "CAMEL_API_KEY=from-file\n")
	t.Setenv("CAMEL_API_KEY", "from-shell")

	applied := LoadEnvFile(path)
	if len(applied) != 0 {
		t.Errorf("applied %v, want nothing", applied)
	}
	if v := os.Getenv("CAMEL_API_KEY"); v != "from-shell" {
		t.Errorf("file overrode the shell: %q", v)
	}
}

// The file carries a PATH for systemd. Applying it to a running process would
// silently drop everything the user's own PATH contains.
func TestLoadEnvFileNeverReplacesPath(t *testing.T) {
	before := os.Getenv("PATH")
	path := writeEnv(t, "PATH=/usr/bin:/bin\n")

	LoadEnvFile(path)

	if after := os.Getenv("PATH"); after != before {
		t.Errorf("PATH was replaced\n before: %s\n  after: %s", before, after)
	}
}

func TestLoadEnvFileMissingFileIsNotAnError(t *testing.T) {
	if got := LoadEnvFile(filepath.Join(t.TempDir(), "absent")); got != nil {
		t.Errorf("missing file applied %v", got)
	}
	if got := LoadEnvFile(""); got != nil {
		t.Errorf("empty path applied %v", got)
	}
}

func TestEnvFilePath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	got := EnvFilePath()
	want := filepath.Join(tmp, "factory", "env")
	if got != want {
		t.Errorf("EnvFilePath = %q, want %q", got, want)
	}
	if !strings.HasSuffix(got, filepath.Join("factory", "env")) {
		t.Errorf("path %q does not sit beside factory.json", got)
	}
}
