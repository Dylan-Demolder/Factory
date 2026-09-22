package config

import "strings"

// SecretEnv reports whether an environment variable's value must be withheld
// from a response that a browser will render.
//
// The key name is checked first, then the shape of the value as a backstop: a
// credential stored under an innocent-looking key must still be caught.
func SecretEnv(key, value string) bool {
	u := strings.ToUpper(key)
	// A *_ENV var names another variable (CAMEL_API_KEY_ENV); it is not itself
	// a credential, and hiding it would only confuse the editor.
	if strings.HasSuffix(u, "_ENV") {
		return false
	}
	for _, marker := range []string{"SECRET", "PASSWORD", "PASSWD", "TOKEN", "API_KEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if strings.Contains(u, marker) {
			return true
		}
	}
	if strings.HasSuffix(u, "_KEY") || u == "KEY" {
		return true
	}
	return secretValue(value)
}

// secretValue recognises credential-shaped values regardless of key name.
func secretValue(v string) bool {
	for _, prefix := range []string{"qaml_live_", "sk-", "sk_live_", "ghp_", "gho_", "github_pat_", "AKIA", "xoxb-", "xoxp-", "glpat-"} {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}
