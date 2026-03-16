// Package unsandbox wraps the un-inception Go SDK for orchestra's use.
// The core client comes from github.com/russellballestrini/un-inception.
// This package adds InjectDirectory (tarball upload) on top.
package unsandbox

import (
	"context"

	un "github.com/russellballestrini/un-inception/clients/go/sync/src"
)

// Client wraps the un-inception Client and adds orchestra-specific methods.
type Client struct {
	*un.Client
}

// Re-export types from the SDK for convenience.
type ExecuteResult = un.ExecuteResult
type SessionResult = un.SessionResult

// NewClient creates a client from explicit credentials.
func NewClient(publicKey, secretKey string) *Client {
	return &Client{Client: un.NewClient(publicKey, secretKey)}
}

// NewClientFromEnv resolves credentials from the 4-tier priority system.
func NewClientFromEnv() (*Client, error) {
	c, err := un.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	return &Client{Client: c}, nil
}

// ShellSession runs a command in an existing session.
// Delegates to un.Client.ShellExec (different method name in SDK).
func (c *Client) ShellSession(ctx context.Context, sessionID, command string) (*ExecuteResult, error) {
	return c.Client.ShellExec(ctx, sessionID, command)
}

// ListSessions returns all active sessions.
func (c *Client) ListSessions(ctx context.Context) ([]map[string]interface{}, error) {
	return c.Client.Sessions(ctx)
}

// ListServices returns all services.
func (c *Client) ListServices(ctx context.Context) ([]map[string]interface{}, error) {
	return c.Client.Services(ctx)
}

// GetServiceLogs retrieves logs for a service.
func (c *Client) GetServiceLogs(ctx context.Context, serviceID string) (string, error) {
	return c.Client.ServiceLogs(ctx, serviceID)
}

// DeleteSession destroys a session.
func (c *Client) DeleteSession(ctx context.Context, sessionID string) error {
	return c.Client.DestroySession(ctx, sessionID)
}

// ValidateKeys checks if the current credentials are valid.
func (c *Client) ValidateKeys(ctx context.Context) (map[string]interface{}, error) {
	return c.Client.CheckKeys(ctx)
}
