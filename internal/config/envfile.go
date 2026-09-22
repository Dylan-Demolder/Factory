package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// EnvFilePath is where agent credentials live: beside factory.json.
//
// The systemd unit loads this file as its EnvironmentFile, which meant the
// terminal workspace only worked if the shell happened to export the same
// variables first — the two entry points disagreed. Reading it here makes a
// plain shell behave like the service.
func EnvFilePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "factory", "env")
}

// LoadEnv applies the env file to this process. See LoadEnvFile.
func LoadEnv() []string {
	return LoadEnvFile(EnvFilePath())
}

// LoadEnvFile reads KEY=value lines from path into the process environment and
// returns the names it set.
//
// Only variables that are not already set are applied. An explicit export
// therefore always wins, and PATH — which the file also carries so systemd
// has one — is never replaced in practice, because a running process already
// has a PATH. That matters: the file's PATH is a short fixed list, and
// overwriting an interactive shell's would silently drop entries such as
// ~/.cargo/bin.
func LoadEnvFile(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil // no env file is a normal state, not an error
	}
	defer f.Close()

	var applied []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue // not an assignment
		}
		key := line[:eq]
		if !isEnvName(key) {
			continue
		}
		val := unquoteEnvValue(strings.TrimSpace(line[eq+1:]))
		if val == "" {
			continue // an empty value tells us nothing; leave it unset
		}
		if _, already := os.LookupEnv(key); already {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			continue
		}
		applied = append(applied, key)
	}
	return applied
}

// isEnvName accepts the POSIX shape: a letter or underscore, then letters,
// digits or underscores. Rejecting anything else keeps a malformed line from
// handing os.Setenv something it would reject anyway.
func isEnvName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// unquoteEnvValue strips one layer of matching quotes, so KEY="value with
// spaces" reads as intended.
func unquoteEnvValue(v string) string {
	if len(v) < 2 {
		return v
	}
	if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
