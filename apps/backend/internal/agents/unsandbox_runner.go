package agents

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/orchestra/orchestra/apps/backend/internal/unsandbox"
)

const ProviderUnsandbox Provider = "unsandbox"

// UnsandboxRunner executes agent turns inside unsandbox.com containers.
// It creates a session, runs the agent command remotely, and streams output
// back through the standard event pipeline.
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

	// Determine what to run
	finalPrompt := strings.TrimSpace(request.Prompt)
	commandLine := r.command
	if strings.TrimSpace(request.CommandOverride) != "" {
		commandLine = strings.TrimSpace(request.CommandOverride)
	}

	var code string
	if commandLine != "" {
		// Run the agent CLI command with the prompt injected
		resolved := strings.ReplaceAll(commandLine, "{{prompt}}", shellQuote(finalPrompt))
		code = resolved
	} else {
		// No command template — treat the prompt as a bash script
		code = finalPrompt
	}

	// Emit start event
	if onEvent != nil {
		onEvent(Event{
			Provider:  ProviderUnsandbox,
			SessionID: sessionID,
			Kind:      "run_started",
			Message:   fmt.Sprintf("executing in unsandbox (network: %s)", r.network),
			Timestamp: time.Now().UTC(),
		})
	}

	// Execute the code in unsandbox
	result, err := r.client.ExecuteWithOpts(ctx, "bash", code, r.network)
	if err != nil {
		if onEvent != nil {
			onEvent(Event{
				Provider:  ProviderUnsandbox,
				SessionID: sessionID,
				Kind:      "error",
				Message:   err.Error(),
				Timestamp: time.Now().UTC(),
			})
		}
		return TurnResult{
			Provider:  ProviderUnsandbox,
			SessionID: sessionID,
			ExitCode:  1,
			Output:    err.Error(),
		}, err
	}

	// Parse the output line by line and emit events
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

	// Emit completion event
	exitCode := 0
	if result.Status == "error" || result.Error != "" {
		exitCode = 1
	}

	if onEvent != nil {
		completionData := map[string]any{
			"status": result.Status,
		}
		if result.JobID != "" {
			completionData["job_id"] = result.JobID
		}
		raw, _ := json.Marshal(completionData)
		var rawMap map[string]any
		_ = json.Unmarshal(raw, &rawMap)

		onEvent(Event{
			Provider:  ProviderUnsandbox,
			SessionID: sessionID,
			Kind:      "turn.completed",
			Message:   fmt.Sprintf("unsandbox execution %s", result.Status),
			Raw:       rawMap,
			Timestamp: time.Now().UTC(),
		})
	}

	return TurnResult{
		Provider:  ProviderUnsandbox,
		SessionID: sessionID,
		ExitCode:  exitCode,
		Output:    output,
		Usage:     collector.usage(),
	}, nil
}
