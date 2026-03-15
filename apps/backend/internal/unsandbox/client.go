// Package unsandbox wraps the un-go-async SDK for Orchestra integration.
//
// Uses github.com/unsandbox/un-go-async which provides HMAC-SHA256 auth,
// credential resolution, and the full unsandbox.com API surface.
//
// To swap for GitLab source later, change the go.mod replace directive:
//   replace github.com/unsandbox/un-go-async => git.unturf.com/engineering/unturf/un-inception/clients/go/async
package unsandbox

import (
	"context"
	"fmt"

	un "github.com/unsandbox/un-go-async/src"
)

// Client wraps the un-go-async SDK with context-aware methods for Orchestra.
type Client struct {
	creds *un.Credentials
}

// NewClient creates a client from explicit credentials.
func NewClient(publicKey, secretKey string) *Client {
	creds, _ := un.ResolveCredentials(publicKey, secretKey)
	return &Client{creds: creds}
}

// NewClientFromEnv resolves credentials from the 4-tier priority system.
func NewClientFromEnv() (*Client, error) {
	creds, err := un.ResolveCredentials("", "")
	if err != nil {
		return nil, fmt.Errorf("unsandbox: %w", err)
	}
	return &Client{creds: creds}, nil
}

// ExecuteResult holds the response from code execution.
type ExecuteResult struct {
	JobID  string
	Status string
	Output string
	Error  string
	Raw    map[string]interface{}
}

// Execute runs code synchronously (blocks until completion).
func (c *Client) Execute(ctx context.Context, language, code string) (*ExecuteResult, error) {
	return c.ExecuteWithOpts(ctx, language, code, "")
}

// ExecuteWithOpts runs code with optional network mode.
func (c *Client) ExecuteWithOpts(ctx context.Context, language, code, network string) (*ExecuteResult, error) {
	ch := un.ExecuteCode(c.creds, language, code)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}

	result := &ExecuteResult{Raw: res.Data}
	if v, ok := res.Data["job_id"].(string); ok {
		result.JobID = v
	}
	if v, ok := res.Data["status"].(string); ok {
		result.Status = v
	}
	if v, ok := res.Data["output"].(string); ok {
		result.Output = v
	}
	if v, ok := res.Data["error"].(string); ok {
		result.Error = v
	}
	return result, nil
}

// WaitForJob polls a job until completion.
func (c *Client) WaitForJob(ctx context.Context, jobID string) (*ExecuteResult, error) {
	ch := un.WaitForJob(c.creds, jobID, 0)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}

	result := &ExecuteResult{Raw: res.Data, JobID: jobID}
	if v, ok := res.Data["status"].(string); ok {
		result.Status = v
	}
	if v, ok := res.Data["output"].(string); ok {
		result.Output = v
	}
	if v, ok := res.Data["error"].(string); ok {
		result.Error = v
	}
	return result, nil
}

// Session represents an unsandbox session.
type Session struct {
	ID     string
	Status string
	Raw    map[string]interface{}
}

// CreateSession creates a new execution session.
func (c *Client) CreateSession(ctx context.Context, language string, network string) (*Session, error) {
	ch := un.CreateSession(c.creds, nil)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}

	s := &Session{Raw: res.Data}
	if v, ok := res.Data["id"].(string); ok {
		s.ID = v
	}
	if v, ok := res.Data["session_id"].(string); ok && s.ID == "" {
		s.ID = v
	}
	if v, ok := res.Data["status"].(string); ok {
		s.Status = v
	}
	return s, nil
}

// ShellSession runs a command in an existing session.
func (c *Client) ShellSession(ctx context.Context, sessionID, command string) (*ExecuteResult, error) {
	ch := un.ShellSession(c.creds, sessionID, command)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}

	result := &ExecuteResult{Raw: res.Data}
	if v, ok := res.Data["output"].(string); ok {
		result.Output = v
	}
	if v, ok := res.Data["status"].(string); ok {
		result.Status = v
	}
	return result, nil
}

// DeleteSession destroys a session.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	ch := un.DeleteSession(c.creds, sessionID)
	res := <-ch
	return res.Err
}

// ListSessions returns all active sessions.
func (c *Client) ListSessions(ctx context.Context) ([]map[string]interface{}, error) {
	ch := un.ListSessions(c.creds)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}
	return res.Sessions, nil
}

// ListServices returns all services.
func (c *Client) ListServices(ctx context.Context) ([]map[string]interface{}, error) {
	ch := un.ListServices(c.creds)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}
	return res.Services, nil
}

// ValidateKeys checks if the current credentials are valid.
func (c *Client) ValidateKeys(ctx context.Context) (map[string]interface{}, error) {
	ch := un.ValidateKeys(c.creds)
	res := <-ch
	if res.Err != nil {
		return nil, res.Err
	}
	return res.Data, nil
}
