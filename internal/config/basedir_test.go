package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig drops a valid factory.json in dir, the way `factory init`
// does, and fails if the directory cannot be created.
func writeConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "factory.json"), []byte(ExampleJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// On macOS os.UserConfigDir returns ~/Library/Application Support, which put
// factory.json (written to ~/.config/factory by `factory init`) somewhere no
// other command looked: the quick start's `factory doctor` failed with "no
// config found". Config, credentials and token must share one directory
// wherever they live, and that directory must be the one the README names.
func TestBaseDirIsSharedAndDocumented(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir reads this on Windows
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FACTORY_CONFIG", "")

	want := filepath.Join(home, ".config", "factory")

	dir, err := BaseDir()
	if err != nil {
		t.Fatalf("BaseDir: %v", err)
	}
	if dir != want {
		t.Errorf("BaseDir = %q, want %q", dir, want)
	}

	env := EnvFilePath()
	if wantEnv := filepath.Join(want, "env"); env != wantEnv {
		t.Errorf("EnvFilePath = %q, want %q (the credentials file must sit beside factory.json)", env, wantEnv)
	}

	token, err := TokenPath()
	if err != nil {
		t.Fatalf("TokenPath: %v", err)
	}
	if wantToken := filepath.Join(want, "token"); token != wantToken {
		t.Errorf("TokenPath = %q, want %q", token, wantToken)
	}
}

// ResolvePath has to find a config created by the quick start from any
// working directory, not only when the shell is standing in it.
func TestResolvePathFindsConfigFromAnywhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FACTORY_CONFIG", "")

	dir, err := BaseDir()
	if err != nil {
		t.Fatalf("BaseDir: %v", err)
	}
	writeConfig(t, dir)

	elsewhere := t.TempDir()
	t.Chdir(elsewhere)

	got, err := ResolvePath("", "")
	if err != nil {
		t.Fatalf("ResolvePath from %q: %v — factory could not find its own config", elsewhere, err)
	}
	if filepath.Base(got) != "factory.json" {
		t.Errorf("ResolvePath = %q, want factory.json", got)
	}
}

// An explicit XDG_CONFIG_HOME keeps winning, as it does on Linux today.
func TestBaseDirHonoursXDGConfigHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)

	dir, err := BaseDir()
	if err != nil {
		t.Fatalf("BaseDir: %v", err)
	}
	if want := filepath.Join(xdg, "factory"); dir != want {
		t.Errorf("BaseDir = %q, want %q", dir, want)
	}
}

// A relative XDG_CONFIG_HOME is rejected rather than silently resolved
// against the working directory, matching os.UserConfigDir.
func TestBaseDirRejectsRelativeXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/path")
	if _, err := BaseDir(); err == nil {
		t.Error("relative $XDG_CONFIG_HOME accepted, want an error")
	}
}
