package pipeline

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (e *Engine) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = e.root()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}

// ensureRepo initialises git, ignores .factory/, and makes sure HEAD exists.
func (e *Engine) ensureRepo() error {
	if _, err := os.Stat(filepath.Join(e.root(), ".git")); err != nil {
		if _, err := e.git("init", "-q"); err != nil {
			return err
		}
	}
	gi := filepath.Join(e.root(), ".gitignore")
	data, _ := os.ReadFile(gi)
	if !strings.Contains(string(data), ".factory/") {
		content := string(data)
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += ".factory/\n"
		if err := os.WriteFile(gi, []byte(content), 0o644); err != nil {
			return err
		}
	}
	if _, err := e.git("rev-parse", "--verify", "-q", "HEAD"); err != nil {
		_, err := e.commit("factory: initial commit")
		return err
	}
	return nil
}

func (e *Engine) identityArgs() []string {
	if out, err := e.git("config", "user.email"); err == nil && strings.TrimSpace(out) != "" {
		return nil
	}
	return []string{"-c", "user.name=factory", "-c", "user.email=factory@localhost"}
}

// commit stages everything and commits; returns the short hash ("" if
// there was nothing to commit).
func (e *Engine) commit(msg string) (string, error) {
	if _, err := e.git("add", "-A"); err != nil {
		return "", err
	}
	if out, _ := e.git("status", "--porcelain"); strings.TrimSpace(out) == "" {
		if _, err := e.git("rev-parse", "--verify", "-q", "HEAD"); err == nil {
			return "", nil
		}
	}
	args := append(e.identityArgs(), "commit", "-q", "--allow-empty", "--no-verify", "-m", msg)
	if _, err := e.git(args...); err != nil {
		return "", err
	}
	out, err := e.git("rev-parse", "--short", "HEAD")
	return strings.TrimSpace(out), err
}

// stagedDiff returns a stat summary and the (truncated) diff of all pending
// changes against HEAD.
func (e *Engine) stagedDiff(limit int) (stat, diff string) {
	e.git("add", "-A")
	stat, _ = e.git("diff", "--cached", "--stat")
	diff, _ = e.git("diff", "--cached")
	if len(diff) > limit {
		diff = diff[:limit] + fmt.Sprintf("\n… [diff truncated, %d more bytes]", len(diff)-limit)
	}
	return strings.TrimSpace(stat), diff
}

// discard saves pending changes as a patch file and resets to HEAD so a
// blocked task cannot poison later ones.
func (e *Engine) discard(patchName string) error {
	e.git("add", "-A")
	if diff, err := e.git("diff", "--cached", "--binary"); err == nil && strings.TrimSpace(diff) != "" {
		if err := e.Store.Write(patchName, diff); err != nil {
			return err
		}
	}
	if _, err := e.git("reset", "-q", "--hard"); err != nil {
		return err
	}
	_, err := e.git("clean", "-fdq")
	return err
}

// files lists tracked and untracked (non-ignored) files.
func (e *Engine) files(max int) string {
	out, _ := e.git("ls-files", "--cached", "--others", "--exclude-standard")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > max {
		lines = append(lines[:max], fmt.Sprintf("… and %d more files", len(lines)-max))
	}
	return strings.Join(lines, "\n")
}
