package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Meta is one agent's human metadata — job title, department, tier, notes —
// held in org.json beside factory.json.
//
// It lives outside factory.json because that file is parsed with
// DisallowUnknownFields: any key factory does not know about is rejected, and
// a job title is not factory's business. The web editor, the terminal
// workspace and the factory-agents helper all read and write this one shape.
type Meta struct {
	Title    string `json:"title,omitempty"`
	Dept     string `json:"dept,omitempty"`
	Tier     string `json:"tier,omitempty"`
	Notes    string `json:"notes,omitempty"`
	ReadOnly bool   `json:"readonly,omitempty"`
}

// OrgPath is the sidecar path for a config file.
func OrgPath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "org.json")
}

type metaDoc struct {
	Version int             `json:"version"`
	Meta    map[string]Meta `json:"meta"`
}

const metaVersion = 1

// LoadMeta reads the sidecar. A missing or damaged file yields no metadata
// rather than an error: losing job titles must never stop a config loading.
func LoadMeta(cfgPath string) map[string]Meta {
	raw, err := os.ReadFile(OrgPath(cfgPath))
	if err != nil {
		return map[string]Meta{}
	}
	var doc metaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return map[string]Meta{} // a corrupt sidecar is treated as empty
	}
	if doc.Meta == nil {
		return map[string]Meta{}
	}
	return doc.Meta
}

// SaveMeta writes the sidecar alone. The caller is expected to hold the lock
// when it matters; use Save when writing a config at the same time.
func SaveMeta(cfgPath string, meta map[string]Meta) error {
	doc, err := json.MarshalIndent(metaDoc{Version: metaVersion, Meta: meta}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(OrgPath(cfgPath), append(doc, '\n'), 0o644)
}
