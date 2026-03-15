// Package unfirehose writes unfirehose/1.0 JSONL session logs.
//
// This implements the unfirehose/1.0 schema specification for machine learning
// agent session logging. Each session gets its own JSONL file under
// ~/.unfirehose/canonical/orchestra/{session-uuid}.jsonl
//
// See: https://www.npmjs.com/package/@unturf/unfirehose-schema
package unfirehose

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	SchemaVersion = "unfirehose/1.0"
	Harness       = "orchestra"
)

// Logger writes unfirehose/1.0 JSONL to disk.
type Logger struct {
	mu        sync.Mutex
	outputDir string
	files     map[string]*os.File // sessionID -> open file handle
	version   string
}

// NewLogger creates a logger that writes to ~/.unfirehose/canonical/orchestra/.
func NewLogger(version string) (*Logger, error) {
	dir, err := defaultOutputDir()
	if err != nil {
		return nil, fmt.Errorf("unfirehose: output dir: %w", err)
	}
	return &Logger{
		outputDir: dir,
		files:     make(map[string]*os.File),
		version:   version,
	}, nil
}

// NewLoggerWithDir creates a logger that writes to a custom directory.
func NewLoggerWithDir(dir, version string) *Logger {
	return &Logger{
		outputDir: dir,
		files:     make(map[string]*os.File),
		version:   version,
	}
}

// --- Schema Types (unfirehose/1.0) ---

// ContentBlock is a typed content element within a message.
type ContentBlock struct {
	Type       string `json:"type"`                 // "text", "reasoning", "tool-call", "tool-result"
	Text       string `json:"text,omitempty"`        // for text and reasoning blocks
	ToolCallID string `json:"toolCallId,omitempty"`  // for tool-call and tool-result
	ToolName   string `json:"toolName,omitempty"`    // for tool-call and tool-result
	Input      any    `json:"input,omitempty"`       // for tool-call
	Output     any    `json:"output,omitempty"`      // for tool-result
	IsError    bool   `json:"isError,omitempty"`     // for tool-result
}

// Usage tracks token consumption.
type Usage struct {
	InputTokens       int64             `json:"inputTokens,omitempty"`
	OutputTokens      int64             `json:"outputTokens,omitempty"`
	TotalTokens       int64             `json:"totalTokens,omitempty"`
	InputTokenDetails *InputTokenDetail `json:"inputTokenDetails,omitempty"`
}

// InputTokenDetail breaks down input token sources.
type InputTokenDetail struct {
	CacheReadTokens  int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int64 `json:"cacheWriteTokens,omitempty"`
}

// SessionObject is the session envelope (first JSONL line).
type SessionObject struct {
	Schema         string `json:"$schema"`
	Type           string `json:"type"`
	ID             string `json:"id"`
	ProjectID      string `json:"projectId,omitempty"`
	Status         string `json:"status,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
	FirstPrompt    string `json:"firstPrompt,omitempty"`
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harnessVersion,omitempty"`
	Sidechain      bool   `json:"sidechain,omitempty"`
}

// MessageObject is a single message (one JSONL line per message).
type MessageObject struct {
	Schema         string         `json:"$schema"`
	Type           string         `json:"type"`
	ID             string         `json:"id,omitempty"`
	SessionID      string         `json:"sessionId,omitempty"`
	ParentID       string         `json:"parentId,omitempty"`
	Role           string         `json:"role"`
	Timestamp      string         `json:"timestamp,omitempty"`
	Content        []ContentBlock `json:"content"`
	Model          string         `json:"model,omitempty"`
	StopReason     string         `json:"stopReason,omitempty"`
	Provider       string         `json:"provider,omitempty"`
	Usage          *Usage         `json:"usage,omitempty"`
	Subtype        string         `json:"subtype,omitempty"`
	DurationMs     int64          `json:"durationMs,omitempty"`
	Harness        string         `json:"harness,omitempty"`
	HarnessVersion string         `json:"harnessVersion,omitempty"`
}

// --- Logger Methods ---

// StartSession writes the session envelope as the first JSONL line.
func (l *Logger) StartSession(sessionID, projectID, firstPrompt string) error {
	obj := SessionObject{
		Schema:         SchemaVersion,
		Type:           "session",
		ID:             sessionID,
		ProjectID:      slugifyProject(projectID),
		Status:         "active",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
		FirstPrompt:    truncate(firstPrompt, 500),
		Harness:        Harness,
		HarnessVersion: l.version,
	}
	return l.appendJSON(sessionID, obj)
}

// LogMessage writes a message to the session JSONL.
func (l *Logger) LogMessage(msg MessageObject) error {
	if msg.Schema == "" {
		msg.Schema = SchemaVersion
	}
	if msg.Type == "" {
		msg.Type = "message"
	}
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if msg.Harness == "" {
		msg.Harness = Harness
	}
	if msg.HarnessVersion == "" {
		msg.HarnessVersion = l.version
	}
	return l.appendJSON(msg.SessionID, msg)
}

// LogUserMessage is a convenience for logging a user prompt.
func (l *Logger) LogUserMessage(sessionID, text string) error {
	return l.LogMessage(MessageObject{
		SessionID: sessionID,
		Role:      "user",
		Content: []ContentBlock{
			{Type: "text", Text: text},
		},
	})
}

// LogAssistantMessage is a convenience for logging an assistant response.
func (l *Logger) LogAssistantMessage(sessionID, text, model, provider string, usage *Usage) error {
	return l.LogMessage(MessageObject{
		SessionID: sessionID,
		Role:      "assistant",
		Content: []ContentBlock{
			{Type: "text", Text: text},
		},
		Model:    model,
		Provider: provider,
		Usage:    usage,
	})
}

// LogToolCall logs a tool invocation.
func (l *Logger) LogToolCall(sessionID, toolCallID, toolName string, input any) error {
	return l.LogMessage(MessageObject{
		SessionID: sessionID,
		Role:      "assistant",
		Content: []ContentBlock{
			{Type: "tool-call", ToolCallID: toolCallID, ToolName: toolName, Input: input},
		},
	})
}

// LogToolResult logs a tool result.
func (l *Logger) LogToolResult(sessionID, toolCallID, toolName string, output any, isError bool) error {
	return l.LogMessage(MessageObject{
		SessionID: sessionID,
		Role:      "user",
		Content: []ContentBlock{
			{Type: "tool-result", ToolCallID: toolCallID, ToolName: toolName, Output: output, IsError: isError},
		},
	})
}

// CloseSession writes a session close marker and closes the file handle.
func (l *Logger) CloseSession(sessionID string, totalUsage *Usage) error {
	obj := SessionObject{
		Schema:         SchemaVersion,
		Type:           "session",
		ID:             sessionID,
		Status:         "closed",
		Harness:        Harness,
		HarnessVersion: l.version,
	}
	if err := l.appendJSON(sessionID, obj); err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if f, ok := l.files[sessionID]; ok {
		f.Close()
		delete(l.files, sessionID)
	}
	return nil
}

// Close closes all open file handles.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, f := range l.files {
		f.Close()
		delete(l.files, id)
	}
}

// --- Internal ---

func (l *Logger) appendJSON(sessionID string, obj any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return fmt.Errorf("unfirehose: marshal: %w", err)
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	f, ok := l.files[sessionID]
	if !ok {
		if err := os.MkdirAll(l.outputDir, 0755); err != nil {
			return fmt.Errorf("unfirehose: mkdir: %w", err)
		}
		path := filepath.Join(l.outputDir, sessionID+".jsonl")
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("unfirehose: open %s: %w", path, err)
		}
		l.files[sessionID] = f
	}

	_, err = f.Write(data)
	return err
}

func defaultOutputDir() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(u.HomeDir, ".unfirehose", "canonical", "orchestra")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func slugifyProject(path string) string {
	if path == "" {
		return ""
	}
	s := strings.ReplaceAll(path, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	if strings.HasPrefix(s, "-") {
		return s
	}
	return "-" + s
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
