// Package app wires together all subsystems and runs the orchestrad server,
// including the execution worker, refresh loop, garbage collector, and HTTP API.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/orchestra/orchestra/apps/backend/internal/agents"
	"github.com/orchestra/orchestra/apps/backend/internal/api"
	"github.com/orchestra/orchestra/apps/backend/internal/config"
	"github.com/orchestra/orchestra/apps/backend/internal/db"
	"github.com/orchestra/orchestra/apps/backend/internal/logfile"
	"github.com/orchestra/orchestra/apps/backend/internal/mcp"
	"github.com/orchestra/orchestra/apps/backend/internal/observability"
	"github.com/orchestra/orchestra/apps/backend/internal/orchestrator"
	"github.com/orchestra/orchestra/apps/backend/internal/prompt"
	"github.com/orchestra/orchestra/apps/backend/internal/runtime"
	"github.com/orchestra/orchestra/apps/backend/internal/telemetry"
	"github.com/orchestra/orchestra/apps/backend/internal/terminal"
	"github.com/orchestra/orchestra/apps/backend/internal/tools"
	"github.com/orchestra/orchestra/apps/backend/internal/tracker"
	"github.com/orchestra/orchestra/apps/backend/internal/unfirehose"
	trackergithub "github.com/orchestra/orchestra/apps/backend/internal/tracker/github"
	gitutil "github.com/orchestra/orchestra/apps/backend/internal/utils/git"
	ghutil "github.com/orchestra/orchestra/apps/backend/internal/utils/github"
	"github.com/orchestra/orchestra/apps/backend/internal/tracker/memory"
	trackersqlite "github.com/orchestra/orchestra/apps/backend/internal/tracker/sqlite"
	"github.com/orchestra/orchestra/apps/backend/internal/workspace"
	"github.com/rs/zerolog"
)

// Run initializes all subsystems (database, orchestrator, tracker, MCP, telemetry),
// wires up the HTTP API, and starts the orchestrad server with graceful shutdown
// handling. It blocks until the server exits.
// Run is the main entry point for the orchestrad server. It loads configuration,
// initializes the database, tracker, agent registry, workspace service, and MCP
// registry, then starts background workers and serves the HTTP API until shutdown.
func Run(logger zerolog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if cfg.APIToken == "" && runtime.HostRequiresToken(cfg.Host) {
		return fmt.Errorf("non-loopback host %q requires ORCHESTRA_API_TOKEN", cfg.Host)
	}

	addr := cfg.Host + ":" + cfg.PortString()

	dbPath := filepath.Join(cfg.WorkspaceRoot, ".orchestra", "warehouse.db")
	warehouseDB, err := db.Connect(dbPath)
	if err != nil {
		return fmt.Errorf("connect to warehouse db: %w", err)
	}

	orchestratorService := orchestrator.NewService()
	orchestratorService.SetDB(warehouseDB)
	if err := orchestratorService.RestoreStateFromDB(context.Background()); err != nil {
		logger.Warn().Err(err).Msg("failed to restore orchestrator state from DB")
	}

	orchestratorService.SetStateSets(cfg.ActiveStates, cfg.TerminalStates)
	orchestratorService.SetMaxConcurrent(cfg.MaxConcurrent)
	orchestratorService.SetMaxConcurrentByState(cfg.MaxConcurrentByState)
	orchestratorService.SetMaxTurns(cfg.AgentMaxTurns)

	trackerClient := newTrackerClient(cfg, warehouseDB)
	orchestratorService.SetTrackerClient(trackerClient)
	pubsub := observability.NewPubSub()
	termManager := terminal.NewManager()

	agentRegistry := agents.NewRegistryWithTerminal(cfg.AgentCommands, termManager)
	provider := agents.Provider(cfg.AgentProvider)
	if !agentRegistry.HasProvider(provider) {
		return fmt.Errorf("agent provider %q is not configured", cfg.AgentProvider)
	}
	orchestratorService.SetAgentRegistry(agentRegistry, cfg.AgentCommands, cfg.AgentProvider)

	workspaceService := workspace.Service{Root: cfg.WorkspaceRoot}
	orchestratorService.SetWorkspaceService(workspaceService)
	orchestratorService.SetWorkspaceRoot(cfg.WorkspaceRoot)

	// Initialize MCP (Merge Config + DB)
	allMCPServers := make(map[string]string)
	for k, v := range cfg.MCPServers {
		allMCPServers[k] = v
	}
	if dbServers, err := warehouseDB.ListMCPServers(context.Background()); err == nil {
		for _, s := range dbServers {
			allMCPServers[s.Name] = s.Command
		}
	}

	mcpRegistry := mcp.NewRegistry(allMCPServers, logger)
	mcpRegistry.StartAll(context.Background())
	orchestratorService.SetMCPRegistry(mcpRegistry, allMCPServers)

	logger.Info().Str("agent_provider", cfg.AgentProvider).Str("service_id", runtime.ServiceOrchestrator).Msg("agent provider configured")

	router := api.NewRouterWithPubSub(logger, orchestratorService, &cfg, pubsub, warehouseDB, termManager)

	cleanupTerminalWorkspaces(orchestratorService, trackerClient, workspaceService, cfg.WorkspaceHooks, logger)

	go startGarbageCollector(orchestratorService, warehouseDB, cfg.WorkspaceRoot, cfg.TelemetryRetentionDays, logger)
	go startRefreshWorker(orchestratorService, pubsub, logger)
	go telemetry.StartWatcher(context.Background(), warehouseDB, cfg.ProjectRoots, telemetry.Options{
		Providers:       cfg.TelemetryProviders,
		StoreRawPayload: cfg.TelemetryStoreRawPayload,
	}, logger)

	// Initialize unfirehose/1.0 session logger
	var ufLogger *unfirehose.Logger
	if ufL, err := unfirehose.NewLogger("0.1.0"); err != nil {
		logger.Warn().Err(err).Msg("unfirehose logger disabled")
	} else {
		ufLogger = ufL
		logger.Info().Msg("unfirehose/1.0 session logging enabled")
	}

	toolExecutor := tools.NewLinearToolExecutor(trackerClient)
	go startExecutionWorker(orchestratorService, agentRegistry, provider, cfg.AgentProvider, cfg.WorkspaceRoot, cfg.WorkflowFile, cfg.AgentMaxTurns, toolExecutor.Execute, tools.TrackerToolSpecs(), cfg.WorkspaceHooks, pubsub, warehouseDB, ufLogger, termManager, logger)

	logger.Info().Str("addr", addr).Str("service_id", runtime.ServiceOrchestrator).Msg("starting orchestrad")

	// Use a custom listener with SO_REUSEADDR so the port is available immediately after restart.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	server := &http.Server{Handler: router}

	// Graceful shutdown on SIGTERM/SIGINT so the TUI can restart cleanly.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		logger.Info().Msg("shutting down orchestrad")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()

	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// newTrackerClient builds the appropriate tracker.Client based on the configured
// tracker type (GitHub or SQLite-backed with memory fallback).
func newTrackerClient(cfg config.Config, localDB *db.DB) tracker.Client {
	if strings.ToLower(cfg.TrackerType) == "github" {
		// For GitHub, Endpoint is owner/repo
		parts := strings.Split(cfg.TrackerEndpoint, "/")
		if len(parts) == 2 {
			return trackergithub.NewClient(parts[0], parts[1], cfg.TrackerToken, nil)
		}
	}
	if localDB == nil {
		return memory.NewClient(nil)
	}
	return trackersqlite.NewClient(localDB, cfg.TrackerWorkerAssigneeIDs)
}

// startExecutionWorker runs a tight polling loop that claims runnable issues
// and dispatches them to agents via processExecutionTick.
func startExecutionWorker(
	service *orchestrator.Service,
	registry *agents.Registry,
	provider agents.Provider,
	providerName string,
	workspaceRoot string,
	workflowFile string,
	agentMaxTurns int,
	toolExecutor agents.ToolExecutor,
	toolSpecs []map[string]any,
	workspaceHooks workspace.Hooks,
	pubsub *observability.PubSub,
	warehouseDB *db.DB,
	ufLogger *unfirehose.Logger,
	termManager *terminal.Manager,
	logger zerolog.Logger,
) {
	workspaceService := workspace.Service{Root: workspaceRoot}
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		processExecutionTick(service, workspaceService, registry, provider, providerName, workspaceRoot, workflowFile, agentMaxTurns, toolExecutor, toolSpecs, workspaceHooks, pubsub, warehouseDB, ufLogger, termManager, logger)
	}
}

// processExecutionTick claims the next runnable issue, revalidates it against
// the tracker, prepares the workspace and git branch, renders the prompt,
// executes the agent turn, and handles success, failure, and retry scheduling.
func processExecutionTick(
	service *orchestrator.Service,
	workspaceService workspace.Service,
	registry *agents.Registry,
	provider agents.Provider,
	providerName string,
	workspaceRoot string,
	workflowFile string,
	agentMaxTurns int,
	toolExecutor agents.ToolExecutor,
	toolSpecs []map[string]any,
	workspaceHooks workspace.Hooks,
	pubsub *observability.PubSub,
	warehouseDB *db.DB,
	ufLogger *unfirehose.Logger,
	termManager *terminal.Manager,
	logger zerolog.Logger,
) {
	entry, ok := service.ClaimNextRunnable()
	if !ok {
		return
	}

	shouldDispatch, revalidateErr := service.RevalidateClaimedIssue(context.Background(), entry.IssueID)
	if revalidateErr != nil {
		service.ReleaseClaim(entry.IssueID)
		logger.Warn().Err(revalidateErr).Str("issue_id", entry.IssueID).Msg("issue revalidation failed; skipping dispatch")
		publishSnapshot(pubsub, service)
		return
	}
	if !shouldDispatch {
		logger.Info().Str("issue_id", entry.IssueID).Msg("issue no longer dispatchable after revalidation")
		publishSnapshot(pubsub, service)
		return
	}

	// Backfill title/description/project from tracker if missing (e.g. retry path creates entries without these fields)
	if entry.Title == "" || entry.Description == "" || entry.ProjectID == "" {
		issue, fetchErr := service.FetchIssueByID(context.Background(), entry.IssueID)
		if fetchErr == nil && issue != nil {
			if entry.Title == "" {
				entry.Title = issue.Title
			}
			if entry.Description == "" {
				entry.Description = issue.Description
			}
			if entry.ProjectID == "" {
				entry.ProjectID = issue.ProjectID
			}
		}
	}

	// Resolve provider from entry or configuration
	activeProvider := provider
	activeProviderName := providerName

	if entry.Provider != "" {
		candidate := agents.NormalizeProvider(entry.Provider)
		if registry.HasProvider(candidate) {
			activeProvider = candidate
			activeProviderName = string(candidate)
		}
	} else if entry.AssigneeID != "" {
		// Fallback: Resolve provider from assignee if possible
		p := strings.TrimPrefix(entry.AssigneeID, "agent-")
		candidate := agents.NormalizeProvider(p)
		if registry.HasProvider(candidate) {
			activeProvider = candidate
			activeProviderName = string(candidate)
		}
	}

	// Verify the agent binary is installed before doing any workspace work.
	if err := registry.CheckBinary(activeProvider); err != nil {
		service.ReleaseClaim(entry.IssueID)
		logger.Error().Err(err).Str("issue_id", entry.IssueID).Str("provider", activeProviderName).Msg("agent binary not available; skipping dispatch")
		publishSnapshot(pubsub, service)
		return
	}

	// If the task belongs to a project, use the project's root_path as the workspace
	// so the agent operates on the actual codebase, not an empty temp directory.
	var workspacePath string
	var effectiveWorkspaceRoot string
	var created bool
	var createRes workspace.HookResult
	var err error
	var resolvedProject db.Project
	if entry.ProjectID != "" && warehouseDB != nil {
		project, projErr := warehouseDB.GetProjectByID(context.Background(), entry.ProjectID)
		resolvedProject = project
		if projErr != nil {
			logger.Warn().Err(projErr).Str("issue_id", entry.IssueID).Str("project_id", entry.ProjectID).Msg("failed to lookup project for workspace")
		} else if project.RootPath == "" || !filepath.IsAbs(project.RootPath) {
			logger.Warn().Str("issue_id", entry.IssueID).Str("root_path", project.RootPath).Msg("project root path is empty or not absolute")
		} else if info, statErr := os.Stat(project.RootPath); statErr != nil || !info.IsDir() {
			logger.Warn().Str("issue_id", entry.IssueID).Str("root_path", project.RootPath).Msg("project root path does not exist or is not a directory")
		} else {
			workspacePath = project.RootPath
			effectiveWorkspaceRoot = filepath.Dir(project.RootPath)
			logger.Info().Str("issue_id", entry.IssueID).Str("project_path", workspacePath).Msg("using project root as workspace")
		}
	} else {
		logger.Info().Str("issue_id", entry.IssueID).Str("project_id", entry.ProjectID).Bool("db_nil", warehouseDB == nil).Msg("skipping project workspace lookup")
	}
	if effectiveWorkspaceRoot == "" {
		effectiveWorkspaceRoot = workspaceRoot
	}

	if workspacePath == "" {
		// Fallback to the generated workspace if no project path available
		publishLifecycleEvent(pubsub, "HOOK_STARTED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_create"})
		var ensureErr error
		workspacePath, created, createRes, ensureErr = workspaceService.EnsureIssueWorkspace(entry.IssueIdentifier, activeProviderName, workspaceHooks)
		if ensureErr != nil {
			err = ensureErr
		}
	} else {
		publishLifecycleEvent(pubsub, "HOOK_STARTED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_create"})
		publishLifecycleEvent(pubsub, "HOOK_COMPLETED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_create", "reused": true})
	}
	if err != nil {
		publishLifecycleEvent(pubsub, "HOOK_FAILED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_create", "error": err.Error(), "output": createRes.Output})
		attempt := entry.TurnCount + 1
		dueAt := service.NextRetryDue(entry.IssueID, attempt)
		publishLifecycleEvent(pubsub, "RUN_FAILED", map[string]any{
			"issue_id":         entry.IssueID,
			"issue_identifier": entry.IssueIdentifier,
			"provider":         activeProviderName,
			"attempt":          attempt,
			"error":            err.Error(),
			"cause":            "workspace_prepare_failed",
		})
		if service.ShouldRetryAttempt(attempt) {
			publishLifecycleEvent(pubsub, "RETRY_SCHEDULED", map[string]any{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
				"provider":         activeProviderName,
				"attempt":          attempt,
				"due_at":           dueAt.UTC().Format(time.RFC3339),
				"cause":            "workspace_prepare_failed",
			})
		}
		service.RecordRunFailure(entry.IssueID, activeProviderName, entry.IssueIdentifier, attempt, dueAt, err)
		logger.Error().Err(err).Str("issue_id", entry.IssueID).Str("provider", activeProviderName).Msg("workspace preparation failed")
		publishSnapshot(pubsub, service)
		return
	}
	if created {
		publishLifecycleEvent(pubsub, "HOOK_COMPLETED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_create", "output": createRes.Output})
	} else {
		// Even if not created, we mark it as completed since we "ensured" it exists
		publishLifecycleEvent(pubsub, "HOOK_COMPLETED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_create", "reused": true})
	}
	runAfterHook := func() {
		publishLifecycleEvent(pubsub, "HOOK_STARTED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_run"})
		if res, err := workspaceService.RunAfterRunHook(workspacePath, workspaceHooks); err != nil {
			publishLifecycleEvent(pubsub, "HOOK_FAILED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_run", "error": err.Error(), "output": res.Output})
		} else {
			publishLifecycleEvent(pubsub, "HOOK_COMPLETED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "after_run", "output": res.Output})
		}
	}

	if entry.TurnCount == 0 {
		// Start from main to ensure clean state
		if workspacePath != "" {
			if checkoutErr := gitutil.Checkout(context.Background(), workspacePath, "main"); checkoutErr != nil {
				logger.Warn().Err(checkoutErr).Msg("could not checkout main before task start")
			}
		}

		// Record base SHA before any changes
		if workspacePath != "" {
			headCmd := exec.Command("git", "rev-parse", "HEAD")
			headCmd.Dir = workspacePath
			if headOut, headErr := headCmd.Output(); headErr == nil {
				baseSHA := strings.TrimSpace(string(headOut))
				if baseSHA != "" {
					if _, updateErr := service.UpdateIssue(context.Background(), entry.IssueIdentifier, map[string]any{"base_sha": baseSHA}); updateErr != nil {
						logger.Warn().Err(updateErr).Msg("failed to store base SHA on issue")
					}
				}
			}
		}

		// Create a git branch for this task so changes don't collide with other agents
		if workspacePath != "" && entry.ProjectID != "" {
			branchName := strings.ToLower(strings.ReplaceAll(entry.IssueIdentifier, " ", "-"))
			if branchErr := gitutil.CreateBranch(context.Background(), workspacePath, branchName); branchErr != nil {
				// Branch may already exist — try checking it out instead
				checkoutCmd := exec.Command("git", "checkout", branchName)
				checkoutCmd.Dir = workspacePath
				if checkoutErr := checkoutCmd.Run(); checkoutErr != nil {
					logger.Warn().Err(branchErr).Str("branch", branchName).Msg("could not create or checkout branch; continuing on current branch")
				} else {
					logger.Info().Str("branch", branchName).Msg("checked out existing task branch")
				}
			} else {
				logger.Info().Str("branch", branchName).Msg("created task branch")
				// Store branch name on the issue
				if _, updateErr := service.UpdateIssue(context.Background(), entry.IssueIdentifier, map[string]any{"branch_name": branchName}); updateErr != nil {
					logger.Warn().Err(updateErr).Msg("failed to store branch name on issue")
				}
			}
		}

		publishLifecycleEvent(pubsub, "HOOK_STARTED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "before_run"})
		if res, err := workspaceService.RunBeforeRunHook(workspacePath, workspaceHooks); err != nil {
			publishLifecycleEvent(pubsub, "HOOK_FAILED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "before_run", "error": err.Error(), "output": res.Output})
			runAfterHook()
			attempt := entry.TurnCount + 1
			dueAt := service.NextRetryDue(entry.IssueID, attempt)
			publishLifecycleEvent(pubsub, "RUN_FAILED", map[string]any{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
				"provider":         activeProviderName,
				"attempt":          attempt,
				"error":            err.Error(),
				"cause":            "before_run_hook_failed",
			})
			if service.ShouldRetryAttempt(attempt) {
				publishLifecycleEvent(pubsub, "RETRY_SCHEDULED", map[string]any{
					"issue_id":         entry.IssueID,
					"issue_identifier": entry.IssueIdentifier,
					"provider":         activeProviderName,
					"attempt":          attempt,
					"due_at":           dueAt.UTC().Format(time.RFC3339),
					"cause":            "before_run_hook_failed",
				})
			}
			service.RecordRunFailure(entry.IssueID, activeProviderName, entry.IssueIdentifier, attempt, dueAt, err)
			logger.Error().Err(err).Str("issue_id", entry.IssueID).Str("provider", activeProviderName).Msg("workspace before_run hook failed")
			publishSnapshot(pubsub, service)
			return
		}
		publishLifecycleEvent(pubsub, "HOOK_COMPLETED", map[string]any{"issue_id": entry.IssueID, "issue_identifier": entry.IssueIdentifier, "hook_type": "before_run"})
	}

	attempt := entry.TurnCount + 1
	publishLifecycleEvent(pubsub, "RUN_STARTED", map[string]any{
		"issue_id":         entry.IssueID,
		"issue_identifier": entry.IssueIdentifier,
		"provider":         activeProviderName,
		"attempt":          attempt,
	})
	renderedPrompt, promptErr := prompt.Build(workflowFile, prompt.BuildInput{
		Issue:   tracker.Issue{ID: entry.IssueID, Identifier: entry.IssueIdentifier, Title: entry.Title, Description: entry.Description, State: entry.State},
		Attempt: attempt,
	})
	if promptErr != nil {
		logger.Warn().Err(promptErr).Str("issue_id", entry.IssueID).Msg("prompt build failed; using fallback prompt")
		renderedPrompt = buildExecutionPrompt(entry.IssueIdentifier, entry.Title, entry.Description, attempt)
	}

	// On subsequent turns, append prior turn context so agent knows what it already did
	if attempt > 1 && warehouseDB != nil {
		priorContext := buildPriorTurnContext(warehouseDB, entry.IssueID, entry.IssueIdentifier)
		if priorContext != "" {
			renderedPrompt += "\n\n" + priorContext
		}
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.RegisterCancel(entry.IssueID, activeProviderName, cancel)
	defer service.DeregisterCancel(entry.IssueID, activeProviderName)

	var eventsBuffer []agents.Event

	// Fetch MCP tools and resources
	allToolSpecs := make([]map[string]any, 0, len(toolSpecs))
	disabledSet := make(map[string]struct{})
	// Fetch issue to get disabled tools if using SQLite or memory tracker
	// For now, assume trackerClient has populated DisabledTools if applicable
	for _, dt := range entry.DisabledTools {
		disabledSet[strings.ToLower(dt)] = struct{}{}
	}

	for _, ts := range toolSpecs {
		if name, ok := ts["name"].(string); ok {
			if _, disabled := disabledSet[strings.ToLower(name)]; !disabled {
				allToolSpecs = append(allToolSpecs, ts)
			}
		}
	}

	var allResourceSpecs []map[string]any

	if mcpReg := service.GetMCPRegistry(); mcpReg != nil {
		mcpTools, _ := mcpReg.ListTools(runCtx)
		for _, mt := range mcpTools {
			if name, ok := mt["name"].(string); ok {
				if _, disabled := disabledSet[strings.ToLower(name)]; !disabled {
					allToolSpecs = append(allToolSpecs, mt)
				}
			}
		}

		mcpResources, _ := mcpReg.ListResources(runCtx)
		allResourceSpecs = append(allResourceSpecs, mcpResources...)
	}

	// Create a tool executor that can route to MCP
	mcpAwareExecutor := func(tool string, args map[string]any) map[string]any {
		// Check if it's an MCP tool (prefixed with server name)
		if mcpReg := service.GetMCPRegistry(); mcpReg != nil {
			if strings.Contains(tool, "_") {
				parts := strings.SplitN(tool, "_", 2)
				serverName := parts[0]
				toolName := parts[1]
				res, err := mcpReg.ExecuteTool(context.Background(), serverName, toolName, args)
				if err == nil {
					return res
				}
			}
		}
		return toolExecutor(tool, args)
	}

	sessionID := fmt.Sprintf("%s-%d", entry.IssueIdentifier, time.Now().UnixNano())
	_ = logfile.ResetLatestLog(workspaceRoot, entry.IssueIdentifier, sessionID)
	sessionLogPath := filepath.Join(workspaceRoot, "_logs", logfile.Sanitize(entry.IssueIdentifier), "latest.log")
	service.RecordRunArtifact(entry.IssueID, activeProviderName, sessionID, sessionLogPath)

	// Start unfirehose session logging
	if ufLogger != nil {
		if err := ufLogger.StartSession(sessionID, workspaceRoot, renderedPrompt); err != nil {
			logger.Warn().Err(err).Msg("unfirehose: failed to start session")
		}
		_ = ufLogger.LogUserMessage(sessionID, renderedPrompt)
	}

	if warehouseDB != nil {
		// Link session to the task's project — never create new projects from the execution worker
		_ = warehouseDB.RecordSession(context.Background(), sessionID, entry.ProjectID, entry.IssueID, sessionID, activeProviderName, "", "main")
	}

	result, runErr := registry.RunTurn(runCtx, activeProvider, agents.TurnRequest{
		SessionID:       sessionID,
		Workspace:       workspacePath,
		WorkspaceRoot:   effectiveWorkspaceRoot,
		Prompt:          renderedPrompt,
		IssueIdentifier: entry.IssueIdentifier,
		Attempt:         int(attempt),
		Timeout:         30 * time.Minute,
		AutoApprove:     true,
		ToolExecutor:    mcpAwareExecutor,
		ToolSpecs:       allToolSpecs,
		ResourceSpecs:   allResourceSpecs,
	}, func(event agents.Event) {
		service.RecordRunEvent(entry.IssueID, activeProviderName, event)
		publishRunEvent(pubsub, entry, activeProviderName, event)
		eventsBuffer = append(eventsBuffer, event)

		// Persist to database in real-time
		if warehouseDB != nil && event.SessionID != "" {
			// Skip recording PTY echo noise (echoed prompts, shell decorations)
			if event.Kind == "pty" && (event.Message == "" || len(event.Message) < 5) {
				// skip
			} else if event.Kind == "pty" && (strings.Contains(event.Message, "## Instructions") || strings.Contains(event.Message, "## Task") || strings.Contains(event.Message, "step one")) {
				// skip PTY events that are just the echoed prompt
			} else {
				eventID := uuid.New().String()
				raw, _ := json.Marshal(event.Raw)
				_ = warehouseDB.RecordEvent(context.Background(), eventID, event.SessionID, event.Kind, event.Message, raw, int(event.Usage.InputTokens), int(event.Usage.OutputTokens), event.Timestamp.Format(time.RFC3339))
			}
		}

		// Append to log file in real-time
		if event.SessionID != "" {
			line := event.RawLine
			if line == "" && event.Raw != nil {
				// Reconstruct raw line from event data for providers that don't set RawLine
				if raw, err := json.Marshal(event.Raw); err == nil {
					line = string(raw)
				}
			}
			if line != "" {
				_, _ = logfile.AppendToSessionLog(workspaceRoot, entry.IssueIdentifier, event.SessionID, line+"\n")
			}
		}

		// Write to unfirehose/1.0 JSONL — extract structured content from events
		if ufLogger != nil {
			logEventToUnfirehose(ufLogger, sessionID, activeProviderName, event)
		}

		// Log to stdout for TUI visibility
		if event.Message != "" {
			logger.Info().
				Str("issue", entry.IssueIdentifier).
				Str("provider", activeProviderName).
				Str("kind", event.Kind).
				Msg(event.Message)
		}
	})

	if runErr != nil {
		runAfterHook()
		dueAt := service.NextRetryDue(entry.IssueID, attempt)
		publishLifecycleEvent(pubsub, "RUN_FAILED", map[string]any{
			"issue_id":         entry.IssueID,
			"issue_identifier": entry.IssueIdentifier,
			"provider":         activeProviderName,
			"attempt":          attempt,
			"error":            runErr.Error(),
			"cause":            "agent_run_failed",
		})
		if service.ShouldRetryAttempt(attempt) {
			publishLifecycleEvent(pubsub, "RETRY_SCHEDULED", map[string]any{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
				"provider":         activeProviderName,
				"attempt":          attempt,
				"due_at":           dueAt.UTC().Format(time.RFC3339),
				"cause":            "agent_run_failed",
			})
		}
		service.RecordRunFailure(entry.IssueID, activeProviderName, entry.IssueIdentifier, attempt, dueAt, runErr)
		logger.Error().Err(runErr).Str("issue_id", entry.IssueID).Str("provider", activeProviderName).Msg("agent run failed")
		publishSnapshot(pubsub, service)
		return
	}

	service.RecordRunResult(entry.IssueID, activeProviderName, result.SessionID, result.Usage.InputTokens, result.Usage.OutputTokens, result.Usage.TotalTokens)

	continueTurn, checkErr := service.ShouldContinueTurn(context.Background(), entry.IssueID, activeProviderName, attempt, service.GetMaxTurns())
	if checkErr != nil {
		runAfterHook()
		dueAt := service.NextRetryDue(entry.IssueID, attempt)
		publishLifecycleEvent(pubsub, "RUN_FAILED", map[string]any{
			"issue_id":         entry.IssueID,
			"issue_identifier": entry.IssueIdentifier,
			"provider":         providerName,
			"attempt":          attempt,
			"error":            checkErr.Error(),
			"cause":            "continuation_check_failed",
		})
		if service.ShouldRetryAttempt(attempt) {
			publishLifecycleEvent(pubsub, "RETRY_SCHEDULED", map[string]any{
				"issue_id":         entry.IssueID,
				"issue_identifier": entry.IssueIdentifier,
				"provider":         providerName,
				"attempt":          attempt,
				"due_at":           dueAt.UTC().Format(time.RFC3339),
				"cause":            "continuation_check_failed",
			})
		}
		service.RecordRunFailure(entry.IssueID, activeProviderName, entry.IssueIdentifier, attempt, dueAt, checkErr)
		logger.Error().Err(checkErr).Str("issue_id", entry.IssueID).Msg("failed to check turn continuation")
		publishSnapshot(pubsub, service)
		return
	}

	if continueTurn {
		service.PrepareNextTurn(entry.IssueID, activeProviderName, attempt)
		publishLifecycleEvent(pubsub, "RUN_CONTINUES", map[string]any{
			"issue_id":         entry.IssueID,
			"issue_identifier": entry.IssueIdentifier,
			"provider":         providerName,
			"attempt":          attempt,
			"session_id":       result.SessionID,
		})
		logger.Info().Str("issue_id", entry.IssueID).Str("session_id", result.SessionID).Int64("attempt", attempt).Msg("turn completed; continuing")
		publishSnapshot(pubsub, service)
		return
	}

	// Close unfirehose session on success
	if ufLogger != nil {
		_ = ufLogger.CloseSession(sessionID, &unfirehose.Usage{
			InputTokens:  result.Usage.InputTokens,
			OutputTokens: result.Usage.OutputTokens,
			TotalTokens:  result.Usage.TotalTokens,
		})
	}

	service.RecordRunSuccess(entry.IssueID, activeProviderName)

	// Auto-commit agent work on the task branch
	if workspacePath != "" {
		commitMsg := fmt.Sprintf("feat(%s): %s\n\nImplemented by %s agent via Orchestra",
			entry.IssueIdentifier, entry.Title, activeProviderName)
		if commitErr := gitutil.Commit(context.Background(), workspacePath, commitMsg); commitErr != nil {
			logger.Warn().Err(commitErr).Str("issue_id", entry.IssueID).Msg("auto-commit failed (may have no changes)")
		} else {
			logger.Info().Str("issue_id", entry.IssueID).Msg("auto-committed agent work")
		}
	}

	// Push the task branch to remote
	branchName := strings.ToLower(strings.ReplaceAll(entry.IssueIdentifier, " ", "-"))
	if pushErr := gitutil.Push(context.Background(), workspacePath, "origin", branchName); pushErr != nil {
		logger.Warn().Err(pushErr).Msg("auto-push failed (remote may not be configured)")
	} else {
		logger.Info().Str("branch", branchName).Msg("auto-pushed task branch")
	}

	// Close the agent's terminal session now that it's done
	if termManager != nil {
		terminalID := fmt.Sprintf("issue-%s", entry.IssueIdentifier)
		termManager.CloseSession(terminalID)
		logger.Info().Str("terminal_id", terminalID).Msg("closed agent terminal session")
	}

	// Move issue to Review on successful completion
	if _, err := service.UpdateIssue(context.Background(), entry.IssueIdentifier, map[string]any{"state": "Review"}); err != nil {
		logger.Warn().Err(err).Str("issue_id", entry.IssueID).Msg("failed to set issue to Review after success")
	}

	publishLifecycleEvent(pubsub, "RUN_SUCCEEDED", map[string]any{
		"issue_id":         entry.IssueID,
		"issue_identifier": entry.IssueIdentifier,
		"provider":         providerName,
		"attempt":          attempt,
		"session_id":       result.SessionID,
	})

	runAfterHook()

	// Post completion comment to GitHub issue if linked
	if entry.ProjectID != "" && warehouseDB != nil {
		// Extract the last substantive agent message for the summary
		agentSummary := ""
		for i := len(eventsBuffer) - 1; i >= 0; i-- {
			msg := strings.TrimSpace(eventsBuffer[i].Message)
			if msg != "" && len(msg) > 50 {
				agentSummary = msg
				break
			}
		}

		go func() {
			project := resolvedProject
			if project.GitHubOwner == "" || project.GitHubRepo == "" || project.GitHubToken == "" {
				return
			}
			issue, fetchErr := service.FetchIssueByID(context.Background(), entry.IssueID)
			if fetchErr != nil || issue == nil || issue.URL == "" {
				return
			}
			issueNumber := extractGitHubIssueNumber(issue.URL)
			if issueNumber <= 0 {
				return
			}

			// Get git diff stats for the comment
			diffStats := ""
			if project.RootPath != "" {
				cmd := exec.Command("git", "-C", project.RootPath, "diff", "--stat", "HEAD")
				if out, err := cmd.Output(); err == nil && len(out) > 0 {
					diffStats = string(out)
				}
			}

			// Get list of changed files
			changedFiles := ""
			if project.RootPath != "" {
				cmd := exec.Command("git", "-C", project.RootPath, "diff", "--name-only", "HEAD")
				if out, err := cmd.Output(); err == nil && len(out) > 0 {
					changedFiles = strings.TrimSpace(string(out))
				}
				// Also include untracked files
				cmd2 := exec.Command("git", "-C", project.RootPath, "ls-files", "--others", "--exclude-standard")
				if out2, err := cmd2.Output(); err == nil && len(out2) > 0 {
					if changedFiles != "" {
						changedFiles += "\n"
					}
					changedFiles += strings.TrimSpace(string(out2))
				}
			}

			comment := fmt.Sprintf("## %s Agent Run Completed\n\n", strings.ToUpper(activeProviderName))
			comment += fmt.Sprintf("**Issue**: %s\n**Agent**: %s\n**Turns**: %d\n\n", entry.IssueIdentifier, activeProviderName, entry.TurnCount+1)

			if agentSummary != "" {
				comment += "### Summary\n\n" + agentSummary + "\n\n"
			}

			if changedFiles != "" {
				files := strings.Split(changedFiles, "\n")
				comment += "### Files Changed\n\n"
				for _, f := range files {
					f = strings.TrimSpace(f)
					if f != "" {
						comment += "- `" + f + "`\n"
					}
				}
				comment += "\n"
			}

			if diffStats != "" {
				comment += "### Diff Stats\n\n```\n" + strings.TrimSpace(diffStats) + "\n```\n\n"
			}

			comment += "---\n*Automatically posted by [Orchestra](https://github.com/Traves-Theberge/Orchestra)*"

			if err := ghutil.PostIssueComment(context.Background(), project.GitHubOwner, project.GitHubRepo, project.GitHubToken, issueNumber, comment); err != nil {
				logger.Warn().Err(err).Str("issue_id", entry.IssueID).Msg("failed to post GitHub comment")
			} else {
				logger.Info().Str("issue_id", entry.IssueID).Int("github_issue", issueNumber).Msg("posted completion comment to GitHub")
			}
		}()
	}

	logger.Info().Str("issue_id", entry.IssueID).Str("session_id", result.SessionID).Msg("agent run completed — issue moved to Review")
	publishSnapshot(pubsub, service)
}

// buildExecutionPrompt constructs a fallback prompt for the agent when the
// workflow template is unavailable, including task details and attempt number.
func buildExecutionPrompt(issueIdentifier string, title string, description string, attempt int64) string {
	prompt := fmt.Sprintf("You are an autonomous coding agent working on issue **%s**.\n\n## Task\n**%s**\n\n%s", issueIdentifier, title, description)
	prompt += "\n\n## Instructions\n\n1. Write an **Operational Plan** using markdown checkboxes (`- [ ]` pending, `- [x]` done).\n\n2. Work through each step. After completing a step, restate the plan with updated checkboxes.\n\n3. Use all available tools to implement changes.\n\n4. Verify your work compiles/passes. Do NOT stop until all items are checked off."
	prompt += fmt.Sprintf("\n\nAttempt: %d", attempt)
	return prompt
}

// buildPriorTurnContext retrieves recent agent messages from the unified history
// and formats them as context for multi-turn continuation prompts.
func buildPriorTurnContext(warehouseDB *db.DB, issueID string, issueIdentifier string) string {
	history, err := warehouseDB.GetUnifiedHistory(context.Background(), issueID)
	if err != nil || len(history) == 0 {
		return ""
	}

	var agentMessages []string
	for _, h := range history {
		kind, _ := h["kind"].(string)
		msg, _ := h["message"].(string)
		if msg == "" || len(msg) < 20 {
			continue
		}
		// Only include agent messages (not PTY noise, not state changes)
		if kind == "message" || kind == "agent_message" || kind == "item.completed" {
			agentMessages = append(agentMessages, msg)
		}
	}

	if len(agentMessages) == 0 {
		return ""
	}

	// Take the last few messages to keep context manageable
	maxMessages := 5
	if len(agentMessages) > maxMessages {
		agentMessages = agentMessages[len(agentMessages)-maxMessages:]
	}

	ctx := "## Prior Turn Context\n\nYou have already worked on this task in previous turns. Here is what you reported:\n\n"
	for _, msg := range agentMessages {
		// Truncate very long messages
		if len(msg) > 500 {
			msg = msg[:500] + "..."
		}
		ctx += "> " + strings.ReplaceAll(msg, "\n", "\n> ") + "\n\n"
	}
	ctx += "**Continue from where you left off. Do NOT restart from scratch. Check what files already exist before creating new ones.**\n"

	return ctx
}

// extractGitHubIssueNumber parses a GitHub issue URL and returns the issue number.
func extractGitHubIssueNumber(url string) int {
	// Extract issue number from URL like https://github.com/owner/repo/issues/18
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	if len(parts) < 2 {
		return 0
	}
	if parts[len(parts)-2] != "issues" {
		return 0
	}
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0
	}
	return n
}

// cleanupTerminalWorkspaces removes workspaces for issues that have reached a
// terminal state, called once at startup to reclaim disk space.
func cleanupTerminalWorkspaces(service *orchestrator.Service, trackerClient tracker.Client, workspaceService workspace.Service, hooks workspace.Hooks, logger zerolog.Logger) {
	if trackerClient == nil {
		return
	}
	terminalStates := service.TerminalStates()
	issues, err := trackerClient.FetchIssuesByStates(context.Background(), terminalStates)
	if err != nil {
		logger.Warn().Err(err).Msg("startup terminal workspace cleanup skipped")
		return
	}

	for _, issue := range issues {
		if err := workspaceService.RemoveIssueWorkspaces(issue.Identifier, "", hooks); err != nil {
			logger.Warn().Err(err).Str("issue_identifier", issue.Identifier).Msg("startup workspace cleanup failed")
		}
	}
}

// startGarbageCollector runs hourly to prune old database events and remove
// orphaned workspaces that are no longer associated with active runs.
func startGarbageCollector(service *orchestrator.Service, warehouseDB *db.DB, root string, retentionDays int, logger zerolog.Logger) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if retentionDays <= 0 {
				retentionDays = 7
			}
			// Prune old database events (keep 7 days)
			if warehouseDB != nil {
				affected, err := warehouseDB.PruneEvents(context.Background(), retentionDays)
				if err != nil {
					logger.Warn().Err(err).Msg("database pruning failed")
				} else if affected > 0 {
					logger.Info().Int64("count", affected).Msg("pruned old database events")
				}
			}

			active := service.GetActiveWorkspaceIdentifiers()
			activeSet := make(map[string]struct{})
			for _, id := range active {
				activeSet[id] = struct{}{}
			}

			entries, err := os.ReadDir(root)
			if err != nil {
				continue
			}

			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				name := entry.Name()
				if _, ok := activeSet[name]; ok {
					continue
				}

				// Check if it's an orchestra workspace (safety check)
				marker := filepath.Join(root, name, ".orchestra")
				if _, err := os.Stat(marker); os.IsNotExist(err) {
					continue
				}

				// Check age - don't delete brand new workspaces that might be initializing
				info, err := entry.Info()
				if err == nil && time.Since(info.ModTime()) < 2*time.Hour {
					continue
				}

				logger.Info().Str("workspace", name).Msg("cleaning up orphaned workspace")
				_ = os.RemoveAll(filepath.Join(root, name))
			}
		}
	}
}

// startRefreshWorker runs a 1-second polling loop that triggers tracker refresh
// cycles, publishes updated snapshots, and persists orchestrator state to disk.
func startRefreshWorker(service *orchestrator.Service, pubsub *observability.PubSub, logger zerolog.Logger) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	service.QueueRefresh()

	for range ticker.C {
		service.QueueRefresh()
		if !service.RefreshPending() {
			continue
		}
		before := service.Snapshot()

		if err := service.PerformRefresh(context.Background()); err != nil {
			logger.Error().Err(err).Str("service_id", runtime.ServiceOrchestrator).Msg("refresh worker failed")
			publishSnapshot(pubsub, service)
			continue
		}
		after := service.Snapshot()
		publishRefreshRetryLifecycleEvents(pubsub, before, after)
		publishSnapshot(pubsub, service)
		if err := service.PersistStateToDB(context.Background()); err != nil {
			logger.Warn().Err(err).Msg("failed to persist orchestrator state to DB")
		}
	}
}

// publishSnapshot broadcasts the current orchestrator snapshot to all SSE subscribers.
func publishSnapshot(pubsub *observability.PubSub, service *orchestrator.Service) {
	if pubsub == nil || service == nil {
		return
	}
	pubsub.Publish(observability.Event{Type: "snapshot", Data: service.Snapshot()})
}

// publishLifecycleEvent broadcasts a named lifecycle event (e.g. RUN_STARTED,
// RUN_FAILED, RETRY_SCHEDULED) to all SSE subscribers.
func publishLifecycleEvent(pubsub *observability.PubSub, eventType string, data map[string]any) {
	if pubsub == nil {
		return
	}
	if strings.TrimSpace(eventType) == "" {
		return
	}
	pubsub.Publish(observability.Event{Type: eventType, Data: data})
}

// publishRunEvent broadcasts an individual agent event (tool use, message, etc.)
// to all SSE subscribers along with issue and provider context.
func publishRunEvent(pubsub *observability.PubSub, entry orchestrator.RunningEntry, providerName string, event agents.Event) {
	if pubsub == nil {
		return
	}

	pubsub.Publish(observability.Event{Type: "RUN_EVENT", Data: map[string]any{
		"issue_id":         entry.IssueID,
		"issue_identifier": entry.IssueIdentifier,
		"provider":         providerName,
		"event":            event,
	}})
}

// publishRefreshRetryLifecycleEvents compares snapshots before and after a refresh
// cycle and emits RUN_FAILED and RETRY_SCHEDULED events for newly added retries.
func publishRefreshRetryLifecycleEvents(pubsub *observability.PubSub, before orchestrator.Snapshot, after orchestrator.Snapshot) {
	if pubsub == nil {
		return
	}

	existing := map[string]struct{}{}
	for _, retry := range before.Retrying {
		existing[retryLifecycleKey(retry)] = struct{}{}
	}

	for _, retry := range after.Retrying {
		key := retryLifecycleKey(retry)
		if _, ok := existing[key]; ok {
			continue
		}
		publishLifecycleEvent(pubsub, "RUN_FAILED", map[string]any{
			"issue_id":         retry.IssueID,
			"issue_identifier": retry.IssueIdentifier,
			"attempt":          retry.Attempt,
			"error":            retry.Error,
			"source":           "refresh",
			"cause":            classifyRefreshRetryCause(retry.Error),
		})
		publishLifecycleEvent(pubsub, "RETRY_SCHEDULED", map[string]any{
			"issue_id":         retry.IssueID,
			"issue_identifier": retry.IssueIdentifier,
			"attempt":          retry.Attempt,
			"due_at":           retry.DueAt,
			"source":           "refresh",
			"error":            retry.Error,
			"cause":            classifyRefreshRetryCause(retry.Error),
		})
	}
}

// retryLifecycleKey produces a deduplication key for a retry entry based on
// issue ID, attempt number, and error message.
func retryLifecycleKey(entry orchestrator.RetryEntry) string {
	return strings.TrimSpace(entry.IssueID) + "|" + fmt.Sprintf("%d", entry.Attempt) + "|" + strings.TrimSpace(entry.Error)
}

// logEventToUnfirehose extracts structured content from agent events and writes
// proper unfirehose/1.0 messages — text, tool calls, tool results, metrics.
func logEventToUnfirehose(ufLogger *unfirehose.Logger, sessionID, provider string, event agents.Event) {
	raw := event.Raw
	if raw == nil && event.Message == "" {
		return
	}

	kind := event.Kind

	// Claude result event — final output with full usage
	if kind == "result" || (raw != nil && firstStr(raw, "type") == "result") {
		resultText := firstStr(raw, "result")
		model := firstStr(raw, "model")
		stopReason := firstStr(raw, "stop_reason", "stopReason")
		costStr := ""
		if cost, ok := raw["total_cost_usd"].(float64); ok && cost > 0 {
			costStr = fmt.Sprintf("%.6f", cost)
		}

		// Extract usage from the result
		u := extractUnfirehoseUsage(raw)

		if resultText != "" {
			msg := map[string]any{
				"sessionId": sessionID,
				"role":      "assistant",
				"content":   []unfirehose.ContentBlock{unfirehose.TextBlock(resultText)},
				"provider":  provider,
				"usage":     u,
			}
			if model != "" {
				msg["model"] = model
			}
			if stopReason != "" {
				msg["stopReason"] = stopReason
			}
			_ = ufLogger.LogMessage(msg)
		}

		// Log cost as a DataPoint metric
		if costStr != "" {
			if cost, ok := raw["total_cost_usd"].(float64); ok {
				_ = ufLogger.LogDataPoint(sessionID, "agent.cost.usd", "count", cost,
					map[string]string{"provider": provider, "session": sessionID},
					0, "dollar")
			}
		}

		// Log token usage as DataPoint
		if u != nil && (u.InputTokens > 0 || u.OutputTokens > 0) {
			_ = ufLogger.LogDataPoint(sessionID, "agent.tokens.input", "count", u.InputTokens,
				map[string]string{"provider": provider}, 0, "token")
			_ = ufLogger.LogDataPoint(sessionID, "agent.tokens.output", "count", u.OutputTokens,
				map[string]string{"provider": provider}, 0, "token")
		}
		return
	}

	// Content block events — text deltas from streaming
	if kind == "content_block_delta" || kind == "content_block_start" {
		if delta := nestedAny(raw, "delta"); delta != nil {
			if text := firstStr(delta, "text"); text != "" {
				_ = ufLogger.LogMessage(map[string]any{
					"sessionId": sessionID,
					"role":      "assistant",
					"content":   []unfirehose.ContentBlock{unfirehose.TextBlock(text)},
					"provider":  provider,
				})
				return
			}
		}
		// Tool use start
		if cb := nestedAny(raw, "content_block"); cb != nil {
			if firstStr(cb, "type") == "tool_use" {
				toolName := firstStr(cb, "name")
				toolID := firstStr(cb, "id")
				if toolName != "" {
					_ = ufLogger.LogToolCall(sessionID, toolID, toolName, cb["input"])
					return
				}
			}
		}
	}

	// Tool result events
	if kind == "tool_result" || (raw != nil && firstStr(raw, "type") == "tool_result") {
		toolID := firstStr(raw, "tool_use_id", "toolCallId")
		toolName := firstStr(raw, "name", "toolName")
		output := firstStr(raw, "output", "content")
		isErr := false
		if v, ok := raw["is_error"].(bool); ok {
			isErr = v
		}
		if toolID != "" {
			_ = ufLogger.LogToolResult(sessionID, toolID, toolName, output, isErr)
			return
		}
	}

	// System/progress events
	if kind == "system" || strings.HasPrefix(kind, "RUN_") || kind == "HOOK_STARTED" || kind == "HOOK_COMPLETED" {
		if event.Message != "" {
			_ = ufLogger.LogSystem(sessionID, kind, map[string]any{"durationMs": event.Usage.TotalTokens})
		}
		return
	}

	// Fallback: if there's a message, log it as assistant text
	if event.Message != "" {
		_ = ufLogger.LogAssistantMessage(sessionID, event.Message, "", provider, &unfirehose.Usage{
			InputTokens:  event.Usage.InputTokens,
			OutputTokens: event.Usage.OutputTokens,
			TotalTokens:  event.Usage.TotalTokens,
		})
	}
}

// extractUnfirehoseUsage pulls token usage from claude's result payload.
func extractUnfirehoseUsage(raw map[string]any) *unfirehose.Usage {
	u := &unfirehose.Usage{}

	// Try nested usage object first, then top-level
	for _, node := range []map[string]any{nestedAny(raw, "usage"), raw} {
		if node == nil {
			continue
		}
		if v := intVal(node, "input_tokens", "inputTokens"); v > 0 {
			u.InputTokens = v
		}
		if v := intVal(node, "output_tokens", "outputTokens"); v > 0 {
			u.OutputTokens = v
		}
		if v := intVal(node, "cache_read_input_tokens", "cacheReadInputTokens"); v > 0 {
			if u.InputTokenDetails == nil {
				u.InputTokenDetails = &unfirehose.InputTokenDetail{}
			}
			u.InputTokenDetails.CacheReadTokens = v
		}
		if v := intVal(node, "cache_creation_input_tokens", "cacheCreationInputTokens"); v > 0 {
			if u.InputTokenDetails == nil {
				u.InputTokenDetails = &unfirehose.InputTokenDetail{}
			}
			u.InputTokenDetails.CacheWriteTokens = v
		}
		if u.InputTokens > 0 || u.OutputTokens > 0 {
			u.TotalTokens = u.InputTokens + u.OutputTokens
			return u
		}
	}

	// Try modelUsage
	if mu, ok := raw["modelUsage"].(map[string]any); ok {
		for _, modelData := range mu {
			if md, ok := modelData.(map[string]any); ok {
				u.InputTokens = intVal(md, "inputTokens")
				u.OutputTokens = intVal(md, "outputTokens")
				if cr := intVal(md, "cacheReadInputTokens"); cr > 0 {
					if u.InputTokenDetails == nil {
						u.InputTokenDetails = &unfirehose.InputTokenDetail{}
					}
					u.InputTokenDetails.CacheReadTokens = cr
				}
				if cw := intVal(md, "cacheCreationInputTokens"); cw > 0 {
					if u.InputTokenDetails == nil {
						u.InputTokenDetails = &unfirehose.InputTokenDetail{}
					}
					u.InputTokenDetails.CacheWriteTokens = cw
				}
				u.TotalTokens = u.InputTokens + u.OutputTokens
				return u
			}
		}
	}

	return nil
}

func nestedAny(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if sub, ok := v.(map[string]any); ok {
			return sub
		}
	}
	return nil
}

func firstStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func intVal(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		}
	}
	return 0
}

func classifyRefreshRetryCause(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(normalized, "stalled run exceeded timeout") || strings.Contains(normalized, "stalled") {
		return "stalled_timeout"
	}
	return "refresh_retry"
}
