package api

import (
	"encoding/json"
	"net/http"

	"github.com/orchestra/orchestra/apps/backend/internal/unsandbox"
)

// PostUnsandboxExecute executes code in an unsandbox container.
// POST /api/v1/unsandbox/execute
// Body: { "language": "python", "code": "print('hello')", "network": "semitrusted" }
func (s *Server) PostUnsandboxExecute(w http.ResponseWriter, r *http.Request) {
	client, err := unsandbox.NewClientFromEnv()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unsandbox_not_configured", err.Error())
		return
	}

	var body struct {
		Language string `json:"language"`
		Code     string `json:"code"`
		Network  string `json:"network"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_body", "expected JSON with language, code fields")
		return
	}
	if body.Language == "" {
		body.Language = "bash"
	}
	if body.Code == "" {
		writeJSONError(w, http.StatusBadRequest, "missing_code", "code field is required")
		return
	}

	result, err := client.ExecuteWithOpts(r.Context(), body.Language, body.Code, body.Network)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "execution_failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": result.Status,
		"output": result.Output,
		"error":  result.Error,
		"job_id": result.JobID,
	})
}

// GetUnsandboxSessions lists active unsandbox sessions.
// GET /api/v1/unsandbox/sessions
func (s *Server) GetUnsandboxSessions(w http.ResponseWriter, r *http.Request) {
	client, err := unsandbox.NewClientFromEnv()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unsandbox_not_configured", err.Error())
		return
	}

	sessions, err := client.ListSessions(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "list_failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessions": sessions,
	})
}

// GetUnsandboxServices lists unsandbox services.
// GET /api/v1/unsandbox/services
func (s *Server) GetUnsandboxServices(w http.ResponseWriter, r *http.Request) {
	client, err := unsandbox.NewClientFromEnv()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unsandbox_not_configured", err.Error())
		return
	}

	services, err := client.ListServices(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "list_failed", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"services": services,
	})
}

// GetUnsandboxStatus checks if unsandbox credentials are configured and valid.
// GET /api/v1/unsandbox/status
func (s *Server) GetUnsandboxStatus(w http.ResponseWriter, r *http.Request) {
	client, err := unsandbox.NewClientFromEnv()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"configured": false,
			"error":      err.Error(),
		})
		return
	}

	keyInfo, err := client.ValidateKeys(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"configured": true,
			"valid":      false,
			"error":      err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"configured": true,
		"valid":      true,
		"key_info":   keyInfo,
	})
}
