package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// This file is the single writer of factory.json. Both editors — the web
// interface and the terminal workspace — go through Save, so the rules about
// withheld secrets and symbolic placeholders are implemented exactly once.

// Normalise turns a document coming back from an editor into factory's
// storage shape.
//
// Agents are presented to editors as a sorted array because that renders well,
// but factory stores them keyed by name; the array is converted back so the
// payload an editor was handed round-trips. The editor's own bookkeeping keys
// are dropped, and nothing else is touched — a typo still fails in Parse.
func Normalise(raw json.RawMessage) (map[string]any, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid config document: %w", err)
	}
	for _, k := range []string{"path", "editable", "broken", "error", "fields", "notify"} {
		delete(doc, k)
	}
	switch agents := doc["agents"].(type) {
	case []any:
		byName := make(map[string]any, len(agents))
		for i, item := range agents {
			a, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("agents[%d] must be an object", i)
			}
			name, _ := a["name"].(string)
			if name == "" {
				return nil, fmt.Errorf("agents[%d] has no name", i)
			}
			if _, dup := byName[name]; dup {
				return nil, fmt.Errorf("duplicate agent %q", name)
			}
			delete(a, "name") // the map key carries it; Agent has no name field
			delete(a, "meta")
			delete(a, "env_secret")
			byName[name] = a
		}
		doc["agents"] = byName
	case map[string]any:
		for _, a := range agents {
			if m, ok := a.(map[string]any); ok {
				delete(m, "meta")
				delete(m, "env_secret")
			}
		}
	}
	return doc, nil
}

// Save validates a config document and writes it, together with the org
// sidecar, atomically and under the lock shared with the factory-agents CLI.
//
// Two properties matter and are enforced here rather than by each caller:
//
//   - the raw document is written, not the parsed result, so {{config_dir}}
//     placeholders stay symbolic instead of freezing into absolute paths;
//   - an env value an editor could not see (JSON null) inherits the value
//     already on disk, so a credential withheld from a browser is never
//     written back as null or blanked.
//
// A rejected document leaves both files untouched. Parse failures surface as
// *ParseError, so callers can map them to individual fields.
func Save(path string, doc map[string]any, meta map[string]Meta) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(abs)

	validate, err := Parse(mustJSON(doc), dir)
	if err != nil {
		return err
	}

	inheritWithheldEnv(doc, abs)
	canonical, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	sanitised := sanitiseMeta(meta, validate.Agents, abs)
	metaBytes, err := json.MarshalIndent(metaDoc{Version: metaVersion, Meta: sanitised}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode org metadata: %w", err)
	}

	return WithLock(dir, func() error {
		if err := writeFileAtomic(abs, canonical, 0o644); err != nil {
			return fmt.Errorf("write config: %w", err)
		}
		if err := writeFileAtomic(OrgPath(abs), append(metaBytes, '\n'), 0o644); err != nil {
			return fmt.Errorf("write org metadata: %w", err)
		}
		return nil
	})
}

// inheritWithheldEnv fills env values the editor could not see: null means
// "unchanged", so it takes what is already on disk instead of writing null —
// which would blank the credential. Unknown keys are dropped rather than
// written as null.
func inheritWithheldEnv(doc map[string]any, cfgPath string) {
	old, err := readConfigEnv(cfgPath)
	if err != nil {
		old = nil
	}
	agents, ok := doc["agents"].(map[string]any)
	if !ok {
		return
	}
	for name, a := range agents {
		agent, ok := a.(map[string]any)
		if !ok {
			continue
		}
		env, ok := agent["env"].(map[string]any)
		if !ok {
			continue
		}
		for k, v := range env {
			if v != nil {
				continue
			}
			if prev, ok := old[name][k]; ok {
				env[k] = prev
			} else {
				delete(env, k)
			}
		}
		if len(env) == 0 {
			delete(agent, "env")
		}
	}
}

// sanitiseMeta keeps the sidecar tidy: trimmed values, only known agents, and
// metadata for agents the caller did not mention is preserved so a partial
// update cannot erase the rest.
func sanitiseMeta(in map[string]Meta, agents map[string]Agent, cfgPath string) map[string]Meta {
	out := map[string]Meta{}
	for name, m := range in {
		if _, ok := agents[name]; !ok {
			continue
		}
		m.Title = strings.TrimSpace(m.Title)
		m.Dept = strings.TrimSpace(m.Dept)
		m.Tier = strings.TrimSpace(m.Tier)
		m.Notes = strings.TrimSpace(m.Notes)
		out[name] = m
	}
	for name, m := range LoadMeta(cfgPath) {
		if _, ok := agents[name]; !ok {
			continue
		}
		if _, sent := out[name]; !sent {
			out[name] = m
		}
	}
	return out
}

// readConfigEnv pulls env maps out of an existing config without validating
// it, so secrets can still be inherited while repairing a config that no
// longer parses.
func readConfigEnv(cfgPath string) (map[string]map[string]string, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Agents map[string]struct {
			Env map[string]string `json:"env"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := make(map[string]map[string]string, len(doc.Agents))
	for name, a := range doc.Agents {
		if len(a.Env) > 0 {
			out[name] = a.Env
		}
	}
	return out, nil
}

// RawFields returns each agent's document exactly as written on disk, before
// {{config_dir}} expansion, so an edit round-trips untouched.
func RawFields(cfgPath string) map[string]map[string]any {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	var doc struct {
		Agents map[string]map[string]any `json:"agents"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return doc.Agents
}

// writeFileAtomic replaces path only once the whole document is on disk, so a
// crash mid-write can never leave a truncated config behind.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil // a map straight out of encoding/json cannot fail to encode
	}
	return b
}
