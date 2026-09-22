package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI drives the real argument parsing and returns everything the
// process would have produced.
func runCLI(args ...string) (stdout, stderr string, code int) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestRunGreetsTheWorldByDefault(t *testing.T) {
	stdout, stderr, code := runCLI()
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if stdout != "Hello, World!\n" {
		t.Errorf("stdout = %q, want %q", stdout, "Hello, World!\n")
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want it empty on success", stderr)
	}
}

func TestRunGreetsEachNameOnItsOwnLine(t *testing.T) {
	stdout, _, code := runCLI("Ada", "Grace")
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	want := "Hello, Ada!\nHello, Grace!\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestRunShoutFlag(t *testing.T) {
	for _, flagName := range []string{"--shout", "-s"} {
		t.Run(flagName, func(t *testing.T) {
			stdout, stderr, code := runCLI(flagName, "Ada")
			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if stdout != "HELLO, ADA!\n" {
				t.Errorf("stdout = %q, want %q", stdout, "HELLO, ADA!\n")
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want it empty on success", stderr)
			}
		})
	}
}

func TestRunCustomSalutation(t *testing.T) {
	for _, tc := range []struct {
		flagName string
		want     string
	}{
		{"-g", "Hi, Ada!\n"},
		{"--salutation", "Howdy, Ada!\n"},
	} {
		t.Run(tc.flagName, func(t *testing.T) {
			word := map[string]string{"-g": "Hi", "--salutation": "Howdy"}[tc.flagName]
			stdout, stderr, code := runCLI(tc.flagName, word, "Ada")
			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want it empty on success", stderr)
			}
		})
	}
}

func TestRunHelpPrintsUsageToStdout(t *testing.T) {
	for _, flagName := range []string{"--help", "-h"} {
		t.Run(flagName, func(t *testing.T) {
			stdout, stderr, code := runCLI(flagName)
			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "greeter [flags]") {
				t.Errorf("stdout = %q, want the usage text", stdout)
			}
			if stderr != "" {
				t.Errorf("stderr = %q, want help on stdout only", stderr)
			}
		})
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	stdout, stderr, code := runCLI("--loud", "Ada")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing on success-less runs", stdout)
	}
	if !strings.Contains(stderr, "flag provided but not defined: -loud") {
		t.Errorf("stderr = %q, want it to name the offending flag", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr = %q, want the usage text alongside the error", stderr)
	}
}

// TestGreeterBinaryEndToEnd builds the actual binary and runs it the way a
// user would, so flag handling, output and exit statuses are checked against
// the real program rather than an in-process stand-in.
func TestGreeterBinaryEndToEnd(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "greeter")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	runBin := func(t *testing.T, args ...string) (stdout, stderr string, code int) {
		t.Helper()
		var out, errOut bytes.Buffer
		cmd := exec.Command(bin, args...)
		cmd.Stdout = &out
		cmd.Stderr = &errOut
		err := cmd.Run()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				t.Fatalf("running %v: %v", args, err)
			}
		}
		return out.String(), errOut.String(), code
	}

	t.Run("greets the world", func(t *testing.T) {
		stdout, _, code := runBin(t)
		if code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
		if stdout != "Hello, World!\n" {
			t.Errorf("stdout = %q, want %q", stdout, "Hello, World!\n")
		}
	})

	t.Run("greets named people with flags", func(t *testing.T) {
		stdout, stderr, code := runBin(t, "-g", "Hi", "--shout", "Ada")
		if code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
		if stdout != "HI, ADA!\n" {
			t.Errorf("stdout = %q, want %q", stdout, "HI, ADA!\n")
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want it empty on success", stderr)
		}
	})

	t.Run("help exits 0", func(t *testing.T) {
		stdout, _, code := runBin(t, "--help")
		if code != 0 {
			t.Errorf("exit = %d, want 0", code)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Errorf("stdout = %q, want the usage text", stdout)
		}
	})

	t.Run("bad flag exits 2 and explains on stderr", func(t *testing.T) {
		stdout, stderr, code := runBin(t, "--loud")
		if code != 2 {
			t.Errorf("exit = %d, want 2", code)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want the error on stderr only", stdout)
		}
		if !strings.Contains(stderr, "not defined") {
			t.Errorf("stderr = %q, want it to name the offending flag", stderr)
		}
	})
}
