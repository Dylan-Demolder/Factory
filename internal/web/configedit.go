package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"

	"github.com/dylan-demolder/factory/internal/config"
)

// Saving a config from the browser is a thin transport concern: parse the
// body, hand the document to config.Save, and translate its error into field
// level messages. The rules about withheld secrets, symbolic {{config_dir}}
// placeholders, validation and locking all live in the config package, shared
// with the terminal workspace.

// agentMeta is metadata about an agent; defined in config alongside the sidecar
// it lives in.
type agentMeta = config.Meta

// putConfigRequest is the body of PUT /api/config.
type putConfigRequest struct {
	Config json.RawMessage      `json:"config"`
	Meta   map[string]agentMeta `json:"meta"`
}

// handleConfigSave validates and stores a config edited in the browser.
func (s *Server) handleConfigSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		writeErr(w, http.StatusMethodNotAllowed, "use PUT")
		return
	}
	path, err := config.ResolvePath(s.opt.ConfigPath, "")
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}

	var body putConfigRequest
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Config) == 0 {
		writeErr(w, http.StatusBadRequest, "config is required")
		return
	}
	doc, err := config.Normalise(body.Config)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.Save(path, doc, body.Meta); err != nil {
		// A document factory rejects is the caller's problem to fix; anything
		// else is ours (permissions, disk).
		if fields := config.Fields(err); fields != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]any{
				"error":  err.Error(),
				"fields": fields,
			})
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	abs, _ := filepath.Abs(path)
	writeJSON(w, map[string]any{"ok": true, "path": abs})
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
