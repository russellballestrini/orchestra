package api

import (
	"encoding/json"
	"net/http"

	"github.com/orchestra/orchestra/apps/backend/internal/unsandbox"
)

// GetSSHConfig returns the current SSH forwarding configuration and the list
// of available private keys found in ~/.ssh/.
// GET /api/v1/config/ssh
func (s *Server) GetSSHConfig(w http.ResponseWriter, r *http.Request) {
	cfg := unsandbox.LoadSSHConfig()
	keys := unsandbox.ListSSHKeys()
	writeJSON(w, http.StatusOK, map[string]any{
		"forward_enabled":  cfg.ForwardEnabled,
		"key_path":         cfg.KeyPath,
		"available_keys":   keys,
	})
}

// PostSSHConfig saves the SSH forwarding configuration.
// POST /api/v1/config/ssh
// Body: { "forward_enabled": true, "key_path": "/home/fox/.ssh/id_ed25519" }
func (s *Server) PostSSHConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ForwardEnabled bool   `json:"forward_enabled"`
		KeyPath        string `json:"key_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_body", "expected JSON with forward_enabled, key_path")
		return
	}

	cfg := unsandbox.SSHConfig{
		ForwardEnabled: body.ForwardEnabled,
		KeyPath:        body.KeyPath,
	}
	if err := unsandbox.SaveSSHConfig(cfg); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save_failed", "failed to save SSH config")
		return
	}

	keys := unsandbox.ListSSHKeys()
	writeJSON(w, http.StatusOK, map[string]any{
		"forward_enabled": cfg.ForwardEnabled,
		"key_path":        cfg.KeyPath,
		"available_keys":  keys,
	})
}
