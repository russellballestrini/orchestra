// Package api implements the Orchestra HTTP API, including RESTful endpoints
// for issue management, agent configuration, project operations, SSE event
// streaming, terminal WebSocket proxying, and GitHub integration.
package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/orchestra/orchestra/apps/backend/internal/config"
	"github.com/orchestra/orchestra/apps/backend/internal/db"
	"github.com/orchestra/orchestra/apps/backend/internal/observability"
	"github.com/orchestra/orchestra/apps/backend/internal/orchestrator"
	"github.com/orchestra/orchestra/apps/backend/internal/staticassets"
	"github.com/orchestra/orchestra/apps/backend/internal/terminal"
	"github.com/rs/zerolog"
)

// Server holds shared dependencies for all HTTP handlers, including the
// logger, orchestrator service, database, pub/sub bus, and configuration.
type Server struct {
	logger        zerolog.Logger
	orchestrator  *orchestrator.Service
	workspaceRoot string
	authToken     string
	pubsub        *observability.PubSub
	db            *db.DB
	config        *config.Config
	termManager   *terminal.Manager
}

// NewRouter creates an http.Handler with the full API route table, using only
// the required dependencies (no pub/sub, warehouse DB, or terminal manager).
func NewRouter(
	logger zerolog.Logger,
	orchestratorService *orchestrator.Service,
	cfg *config.Config,
) http.Handler {
	return NewRouterWithPubSub(logger, orchestratorService, cfg, nil, nil, nil)
}

// NewRouterWithPubSub creates an http.Handler with the full API route table and
// optional pub/sub, warehouse database, and terminal manager dependencies for
// SSE streaming, data persistence, and PTY support.
func NewRouterWithPubSub(
	logger zerolog.Logger,
	orchestratorService *orchestrator.Service,
	cfg *config.Config,
	pubsub *observability.PubSub,
	warehouseDB *db.DB,
	termManager *terminal.Manager,
) http.Handler {
	if termManager == nil {
		termManager = terminal.NewManager()
	}
	server := &Server{
		logger:        logger,
		orchestrator:  orchestratorService,
		workspaceRoot: cfg.WorkspaceRoot,
		authToken:     cfg.APIToken,
		pubsub:        pubsub,
		db:            warehouseDB,
		config:        cfg,
		termManager:   termManager,
	}
	r := chi.NewRouter()

	allowedOrigins := corsAllowedOrigins(cfg.Host)
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(RequestLogger(logger))
	r.Use(RateLimit(20, 60)) // 20 req/s sustained, 60 burst
	r.Use(contentTypeGuard)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	r.MethodNotAllowed(server.methodNotAllowed)
	r.NotFound(server.notFound)

	requiresAuth := strings.TrimSpace(cfg.APIToken) != ""
	var protected chi.Router = r
	if requiresAuth {
		protected = r.With(requireBearerToken(cfg.APIToken))
	}

	r.Get("/", server.GetDashboard)
	r.Get("/healthz", Healthz)
	r.Get("/api/v1/healthz", Healthz)
	r.Get("/api/v1/openapi.yaml", server.GetOpenAPIYAML)
	protected.Get("/api/v1/stt/health", server.GetSTTHealth)
	protected.Post("/api/v1/stt/transcribe", server.PostSTTTranscribe)
	protected.Get("/api/v1/state", server.GetState)
	protected.Get("/api/v1/issues", server.GetIssues)
	protected.Post("/api/v1/issues", server.PostIssue)
	protected.Get("/api/v1/search", server.GetSearch)
	protected.Get("/api/v1/events", server.GetEvents)
	protected.Get("/api/v1/workspace/migration/plan", server.GetWorkspaceMigrationPlan)
	protected.Get("/api/v1/config/agents", server.GetAgentConfig)
	protected.Patch("/api/v1/config/agents", server.PatchAgentConfig)
	protected.Post("/api/v1/config/agents", server.PostAgentConfig)
	protected.Get("/api/v1/config/agents/items", server.GetAgentConfigs)
	protected.Post("/api/v1/config/agents/new", server.PostAgentConfigNew)
	protected.Post("/api/v1/config/agents/items", server.PostAgentConfigUpdate)
	protected.Get("/api/v1/agents", server.GetAgents)
	protected.Get("/api/v1/agents/{provider}/mcp", server.GetProviderMCPServers)
	protected.Post("/api/v1/agents/{provider}/mcp", server.AddProviderMCPServer)
	protected.Delete("/api/v1/agents/{provider}/mcp/{name}", server.DeleteProviderMCPServer)
	protected.Get("/api/v1/agents/{provider}/permissions", server.GetProviderPermissions)
	protected.Post("/api/v1/agents/{provider}/permissions", server.PostProviderPermissions)
	protected.Get("/api/v1/agents/{provider}/model", server.GetProviderModel)
	protected.Post("/api/v1/agents/{provider}/model", server.PostProviderModel)
	protected.Get("/api/v1/agents/{provider}/hooks", server.GetProviderHooks)
	protected.Post("/api/v1/agents/{provider}/hooks", server.PostProviderHooks)

	protected.Get("/api/v1/docs", server.GetDocs)
	protected.Get("/api/v1/docs/*", server.GetDocContent)

	protected.Get("/api/v1/mcp/tools", server.GetMCPTools)
	protected.Get("/api/v1/mcp/servers", server.GetMCPServers)
	protected.Post("/api/v1/mcp/servers", server.PostMCPServer)
	protected.Delete("/api/v1/mcp/servers/{id}", server.DeleteMCPServer)

	r.Get("/api/v1/terminal/{session_id}", server.TerminalWebSocket)

	protected.Get("/api/v1/projects", server.GetProjects)
	protected.Post("/api/v1/projects", server.CreateProject)
	protected.Get("/api/v1/projects/{project_id}/file", server.GetProjectFileContent)
	protected.Get("/api/v1/projects/{project_id}/tree", server.GetProjectFileTree)
	protected.Get("/api/v1/projects/{project_id}/git", server.GetProjectGitStats)
	protected.Get("/api/v1/projects/{project_id}/git/status", server.GetProjectGitStatus)
	protected.Get("/api/v1/projects/{project_id}/git/diff", server.GetProjectGitDiff)
	protected.Post("/api/v1/projects/{project_id}/refresh", server.RefreshProject)
	protected.Get("/api/v1/projects/{project_id}", server.GetProject)
	protected.Delete("/api/v1/projects/{project_id}", server.DeleteProject)
	protected.Post("/api/v1/projects/{project_id}/git/commit", server.PostGitCommit)
	protected.Post("/api/v1/projects/{project_id}/git/push", server.PostGitPush)
	protected.Post("/api/v1/projects/{project_id}/git/pull", server.PostGitPull)
	protected.Post("/api/v1/projects/{project_id}/git/branches", server.PostGitCreateBranch)
	protected.Post("/api/v1/projects/{project_id}/git/checkout", server.PostGitCheckout)
	protected.Delete("/api/v1/projects/{project_id}/git/branches/{branch}", server.DeleteGitBranch)
	protected.Post("/api/v1/projects/{project_id}/git/stage", server.PostGitStage)
	protected.Post("/api/v1/projects/{project_id}/git/unstage", server.PostGitUnstage)
	protected.Post("/api/v1/projects/{project_id}/git/stash", server.PostGitStash)
	protected.Post("/api/v1/projects/{project_id}/git/stash/pop", server.PostGitStashPop)
	protected.Post("/api/v1/projects/{project_id}/git/merge", server.PostGitMerge)
	protected.Post("/api/v1/projects/{project_id}/github/disconnect", server.HandleGitHubDisconnect)
	protected.Get("/api/v1/projects/{project_id}/git/branches", server.GetProjectGitBranches)
	protected.Get("/api/v1/projects/{project_id}/github/issues", server.GetProjectGitHubIssues)
	protected.Post("/api/v1/projects/{project_id}/github/issues", server.CreateProjectGitHubIssue)
	protected.Patch("/api/v1/projects/{project_id}/github/issues/{number}", server.UpdateProjectGitHubIssue)
	protected.Get("/api/v1/projects/{project_id}/github/pulls", server.GetProjectGitHubPulls)
	protected.Get("/api/v1/projects/{project_id}/github/pulls/{number}/diff", server.GetProjectGitHubPullDiff)
	protected.Post("/api/v1/projects/{project_id}/github/pulls", server.CreateProjectGitHubPull)
	protected.Get("/api/v1/projects/{project_id}/github/pulls/{number}/reviews", server.GetPRReviews)
	protected.Post("/api/v1/projects/{project_id}/github/pulls/{number}/reviews", server.PostPRReview)
	protected.Put("/api/v1/projects/{project_id}/github/pulls/{number}/merge", server.PostPRMerge)
	protected.Get("/api/v1/projects/{project_id}/github/pulls/{number}/comments", server.GetPRComments)
	protected.Get("/api/v1/sessions", server.GetSessions)
	protected.Get("/api/v1/sessions/{session_id}", server.GetSessionDetail)
	protected.Post("/api/v1/issues/{issue_identifier}/pr", server.CreateGitHubPR)
	protected.Get("/api/v1/warehouse/stats", server.GetWarehouseStats)
	protected.Get("/api/v1/telemetry/health", server.GetTelemetryHealth)
	r.Get("/api/v1/github/login", server.HandleGitHubLogin)
	r.Get("/api/v1/github/callback", server.HandleGitHubCallback)

	protected.Post("/api/v1/refresh", server.PostRefresh)
	protected.Post("/api/v1/workspace/migrate", server.PostWorkspaceMigrate)

	// Unsandbox remote execution
	protected.Get("/api/v1/unsandbox/status", server.GetUnsandboxStatus)
	protected.Post("/api/v1/unsandbox/execute", server.PostUnsandboxExecute)
	protected.Get("/api/v1/unsandbox/jobs/*", server.GetUnsandboxJob)
	protected.Get("/api/v1/unsandbox/sessions", server.GetUnsandboxSessions)
	protected.Get("/api/v1/unsandbox/services", server.GetUnsandboxServices)

	// Unsandbox configuration (API keys)
	protected.Get("/api/v1/config/unsandbox", server.GetUnsandboxConfig)
	protected.Post("/api/v1/config/unsandbox", server.PostUnsandboxConfig)
	protected.Delete("/api/v1/config/unsandbox", server.DeleteUnsandboxConfig)

	// SSH forwarding configuration
	protected.Get("/api/v1/config/ssh", server.GetSSHConfig)
	protected.Post("/api/v1/config/ssh", server.PostSSHConfig)

	// Agent provider API keys (embedded agent widget)
	protected.Get("/api/v1/config/agent-providers", server.HandleGetAgentProviders)
	protected.Post("/api/v1/config/agent-providers", server.HandleSaveAgentProvider)

	protected.Get("/api/v1/issues/{issue_identifier}", server.GetIssue)
	protected.Get("/api/v1/issues/{issue_identifier}/logs", server.GetIssueLogs)
	protected.Get("/api/v1/issues/{issue_identifier}/history", server.GetIssueHistory)
	protected.Get("/api/v1/issues/{issue_identifier}/diff", server.GetIssueDiff)
	protected.Get("/api/v1/issues/{issue_identifier}/artifacts", server.GetArtifacts)
	protected.Get("/api/v1/issues/{issue_identifier}/artifacts/*", server.GetArtifactContent)
	protected.Patch("/api/v1/issues/{issue_identifier}", server.PatchIssue)
	protected.Delete("/api/v1/issues/{issue_identifier}", server.DeleteIssue)
	protected.Delete("/api/v1/issues/{issue_identifier}/session", server.DeleteIssueSession)

	return r
}

// RequestLogger returns chi-compatible middleware that logs each HTTP request
// with its method, path, status code, and duration.
func RequestLogger(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Custom response writer to capture status code
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			logger.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", ww.Status()).
				Dur("duration", time.Since(start)).
				Msg("request")
		})
	}
}

func (s *Server) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = w.Write([]byte(staticassets.NotFoundHTML))
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeJSONError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(staticassets.NotFoundHTML))
}

func contentTypeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		if contentType == "" {
			next.ServeHTTP(w, r)
			return
		}
		if strings.Contains(contentType, "application/json") {
			next.ServeHTTP(w, r)
			return
		}
		writeJSONError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "content-type must be application/json")
	})
}

// writeJSONError writes a structured JSON error response with the given HTTP
// status code, machine-readable error code, and human-readable message.
func writeJSONError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	}); err != nil {
		// Response already started; nothing else to do but log would be ideal.
		// Caller's logger is unavailable here; this is a best-effort path.
		_, _ = w.Write([]byte(`{"error":{"code":"encode_error","message":"failed to encode error response"}}`))
	}
}

// writeJSON encodes v as JSON into w with the given status code.
// If encoding fails, a plain-text error is written instead.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Header already sent; best-effort fallback
		_, _ = w.Write([]byte(`{"error":{"code":"encode_error","message":"failed to encode response"}}`))
	}
}

func corsAllowedOrigins(host string) []string {
	allowlist := []string{
		"http://127.0.0.1:*",
		"http://localhost:*",
		"http://[::1]:*",
	}

	trimmed := strings.TrimSpace(strings.Trim(host, "[]"))
	if trimmed == "" {
		return allowlist
	}

	if runtimeHostIsLoopback(trimmed) {
		return allowlist
	}

	if parsed, err := url.Parse("http://" + trimmed); err == nil {
		hostname := parsed.Hostname()
		if hostname != "" {
			allowlist = append(allowlist, "http://"+hostname, "https://"+hostname)
		}
	}

	return allowlist
}
