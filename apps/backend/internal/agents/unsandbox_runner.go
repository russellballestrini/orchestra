package agents

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/orchestra/orchestra/apps/backend/internal/unsandbox"
)

const ProviderUnsandbox Provider = "unsandbox"

// UnsandboxRunner executes agent turns inside unsandbox.com containers.
//
// Bootstrap flow (mirrors unfirehose /api/boot):
//  1. Create a persistent session (ubuntu:24.04, semitrusted network)
//  2. Sync credentials into the container (umask 077, chmod 600)
//  3. Clone the git repo if workspace has a remote
//  4. Install the agent CLI if needed
//  5. Run the agent command with the prompt
type UnsandboxRunner struct {
	client  *unsandbox.Client
	command string
	network string // "semitrusted" or "zerotrust"
}

// NewUnsandboxRunner creates a runner that dispatches to unsandbox.
// command is the agent CLI template (e.g. "claude -p {{prompt}} --output-format json").
// If command is empty, the prompt is executed as a bash script.
func NewUnsandboxRunner(client *unsandbox.Client, command string) *UnsandboxRunner {
	return &UnsandboxRunner{
		client:  client,
		command: strings.TrimSpace(command),
		network: "semitrusted",
	}
}

// WithNetwork sets the network mode ("semitrusted" or "zerotrust").
func (r *UnsandboxRunner) WithNetwork(network string) *UnsandboxRunner {
	r.network = network
	return r
}

func (r *UnsandboxRunner) RunTurn(ctx context.Context, request TurnRequest, onEvent EventHandler) (TurnResult, error) {
	sessionID := request.SessionID
	if sessionID == "" {
		sessionID = fmt.Sprintf("unsandbox-%s-%d", request.IssueIdentifier, time.Now().UnixNano())
	}

	emit := func(kind, message string, raw map[string]any) {
		if onEvent != nil {
			onEvent(Event{
				Provider:  ProviderUnsandbox,
				SessionID: sessionID,
				Kind:      kind,
				Message:   message,
				Raw:       raw,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	// 1. Create unsandbox session
	emit("session_creating", fmt.Sprintf("creating unsandbox session (network: %s)", r.network), nil)

	unsandboxSession, err := r.client.CreateSession(ctx, "bash", r.network)
	if err != nil {
		emit("error", fmt.Sprintf("session creation failed: %s", err), nil)
		return TurnResult{Provider: ProviderUnsandbox, SessionID: sessionID, ExitCode: 1, Output: err.Error()}, err
	}

	remoteSessionID := unsandboxSession.ID
	emit("session_created", fmt.Sprintf("unsandbox session %s ready", remoteSessionID), map[string]any{"unsandbox_session_id": remoteSessionID})

	// 2. Bootstrap: sync credentials + clone repo + install agent
	bootstrapScript := r.buildBootstrapScript(request)
	if bootstrapScript != "" {
		emit("bootstrap_started", "bootstrapping container", nil)

		bootResult, err := r.client.ShellSession(ctx, remoteSessionID, bootstrapScript)
		if err != nil {
			emit("bootstrap_failed", fmt.Sprintf("bootstrap error: %s", err), nil)
			// Don't abort — the agent command might still work
		} else if bootResult != nil && bootResult.Output != "" {
			// Emit bootstrap output as events
			scanner := bufio.NewScanner(strings.NewReader(bootResult.Output))
			for scanner.Scan() {
				line := scanner.Text()
				if strings.TrimSpace(line) != "" {
					emit("bootstrap", line, nil)
				}
			}
		}

		emit("bootstrap_completed", "container bootstrapped", nil)
	}

	// 3. Build and execute the agent command
	finalPrompt := strings.TrimSpace(request.Prompt)
	commandLine := r.command
	if strings.TrimSpace(request.CommandOverride) != "" {
		commandLine = strings.TrimSpace(request.CommandOverride)
	}

	var agentCmd string
	if commandLine != "" {
		agentCmd = strings.ReplaceAll(commandLine, "{{prompt}}", shellQuote(finalPrompt))
	} else {
		agentCmd = finalPrompt
	}

	emit("run_started", "executing agent in unsandbox", nil)

	result, err := r.client.ShellSession(ctx, remoteSessionID, agentCmd)
	if err != nil {
		emit("error", err.Error(), nil)
		return TurnResult{
			Provider:  ProviderUnsandbox,
			SessionID: sessionID,
			ExitCode:  1,
			Output:    err.Error(),
		}, err
	}

	// 4. Parse output and emit events
	output := result.Output
	if result.Error != "" && output == "" {
		output = result.Error
	}

	collector := &outputCollector{}
	if output != "" {
		scanner := bufio.NewScanner(strings.NewReader(output))
		for scanner.Scan() {
			line := scanner.Text()
			collector.append(line)

			event := parseLineToEvent(ProviderUnsandbox, "stdout", line)
			event.SessionID = sessionID
			if onEvent != nil {
				onEvent(event)
			}
			collector.mergeUsage(event.Usage)
		}
	}

	// 5. Emit completion
	exitCode := 0
	if result.Status == "error" || result.Error != "" {
		exitCode = 1
	}

	completionRaw := map[string]any{
		"status":               result.Status,
		"unsandbox_session_id": remoteSessionID,
	}
	if result.JobID != "" {
		completionRaw["job_id"] = result.JobID
	}
	emit("turn.completed", fmt.Sprintf("unsandbox execution %s", result.Status), completionRaw)

	return TurnResult{
		Provider:  ProviderUnsandbox,
		SessionID: sessionID,
		ExitCode:  exitCode,
		Output:    output,
		Usage:     collector.usage(),
	}, nil
}

// buildBootstrapScript generates the container setup script.
// Mirrors the unfirehose bootstrap pattern: credentials, repo, agent install.
func (r *UnsandboxRunner) buildBootstrapScript(request TurnRequest) string {
	var parts []string
	parts = append(parts, "#!/bin/bash", "set -e")

	// Sync Claude credentials (if available locally)
	if credScript := syncCredentials(); credScript != "" {
		parts = append(parts, credScript)
	}

	// Clone the workspace repo if we can detect a git remote
	if request.Workspace != "" {
		repoURL := detectGitRemote(request.Workspace)
		repoName := filepath.Base(request.Workspace)
		workDir := "/workspace/" + repoName

		if repoURL != "" {
			parts = append(parts,
				fmt.Sprintf("git clone '%s' '%s' 2>&1 || mkdir -p '%s'", repoURL, workDir, workDir),
				fmt.Sprintf("cd '%s'", workDir),
			)
		} else {
			parts = append(parts, fmt.Sprintf("mkdir -p '%s' && cd '%s'", workDir, workDir))
		}
	}

	// Install claude CLI if the command references it
	if strings.Contains(r.command, "claude") {
		parts = append(parts,
			`export PATH="$HOME/.local/bin:$PATH"`,
			"if ! command -v claude >/dev/null 2>&1; then",
			"  curl -fsSL https://claude.ai/install.sh | bash 2>&1",
			"fi",
			`grep -q ".local/bin" ~/.bashrc 2>/dev/null || echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc`,
		)
	}

	if len(parts) <= 2 {
		return "" // nothing to bootstrap
	}
	return strings.Join(parts, "\n")
}

// syncCredentials reads local Claude credentials and returns shell commands
// to inject them into the container with secure permissions.
func syncCredentials() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}

	credFile := filepath.Join(u.HomeDir, ".claude", ".credentials.json")
	data, err := os.ReadFile(credFile)
	if err != nil {
		return ""
	}

	b64 := base64.StdEncoding.EncodeToString(data)
	lines := []string{
		"umask 077 && mkdir -p ~/.claude",
		fmt.Sprintf("echo '%s' | base64 -d > ~/.claude/.credentials.json", b64),
		"chmod 600 ~/.claude/.credentials.json",
		"chmod 700 ~/.claude",
	}

	// Settings files (non-fatal)
	for _, name := range []string{"settings.json", "settings.local.json"} {
		settingsPath := filepath.Join(u.HomeDir, ".claude", name)
		sData, err := os.ReadFile(settingsPath)
		if err != nil {
			continue
		}
		sB64 := base64.StdEncoding.EncodeToString(sData)
		lines = append(lines, fmt.Sprintf("echo '%s' | base64 -d > ~/.claude/%s", sB64, name))
	}

	return strings.Join(lines, "\n")
}

// detectGitRemote tries to find the origin remote URL for a workspace path.
func detectGitRemote(workspacePath string) string {
	// Read .git/config and find the origin remote URL
	gitConfig := filepath.Join(workspacePath, ".git", "config")
	data, err := os.ReadFile(gitConfig)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	inOrigin := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == `[remote "origin"]` {
			inOrigin = true
			continue
		}
		if inOrigin && strings.HasPrefix(trimmed, "url = ") {
			return strings.TrimPrefix(trimmed, "url = ")
		}
		if strings.HasPrefix(trimmed, "[") {
			inOrigin = false
		}
	}
	return ""
}
