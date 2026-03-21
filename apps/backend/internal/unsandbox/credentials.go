package unsandbox

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// SyncClaudeCredentials reads local Claude credentials and returns shell
// commands to inject them into a container with secure permissions.
// Returns empty string if credentials are not found (non-fatal).
func SyncClaudeCredentials() string {
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
		"chmod 700 ~/.claude",
		fmt.Sprintf("printf '%%s' %s | base64 -d > ~/.claude/.credentials.json", shellQuote(b64)),
		"chmod 600 ~/.claude/.credentials.json",
	}

	// Settings files (non-fatal)
	for _, name := range []string{"settings.json", "settings.local.json"} {
		settingsPath := filepath.Join(u.HomeDir, ".claude", name)
		sData, err := os.ReadFile(settingsPath)
		if err != nil {
			continue
		}
		sB64 := base64.StdEncoding.EncodeToString(sData)
		lines = append(lines, fmt.Sprintf("printf '%%s' %s | base64 -d > ~/.claude/%s", shellQuote(sB64), shellQuote(name)))
	}

	return strings.Join(lines, "\n")
}

// SyncSSHKey reads the user's SSH config preferences and returns shell commands
// to inject the selected private key and supporting files into a container with
// secure permissions.
//
// Returns ("", nil) when forwarding is disabled — intentional, not an error.
// Returns ("", error) when forwarding is enabled but the key cannot be resolved
// or read. Callers must treat this as a hard failure and not proceed.
func SyncSSHKey() (string, error) {
	cfg := LoadSSHConfig()
	if !cfg.ForwardEnabled {
		return "", nil
	}

	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("ssh key sync: cannot determine home directory: %w", err)
	}
	sshDir := filepath.Join(u.HomeDir, ".ssh")

	// Resolve which key to use — explicit path takes priority over auto-detect.
	var keyPath string
	if cfg.KeyPath != "" {
		keyPath = cfg.KeyPath
	} else {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			p := filepath.Join(sshDir, name)
			if _, err := os.Stat(p); err == nil {
				keyPath = p
				break
			}
		}
	}
	if keyPath == "" {
		return "", fmt.Errorf("ssh key sync: forwarding is enabled but no SSH key found in %s", sshDir)
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("ssh key sync: cannot read key %s: %w", keyPath, err)
	}
	if len(keyData) == 0 {
		return "", fmt.Errorf("ssh key sync: key file %s is empty", keyPath)
	}
	keyName := filepath.Base(keyPath)

	lines := []string{
		"umask 077 && mkdir -p ~/.ssh",
		"chmod 700 ~/.ssh",
		fmt.Sprintf("printf '%%s' %s | base64 -d > ~/.ssh/%s", shellQuote(base64.StdEncoding.EncodeToString(keyData)), shellQuote(keyName)),
		fmt.Sprintf("chmod 600 ~/.ssh/%s", shellQuote(keyName)),
	}

	// Sync known_hosts so StrictHostKeyChecking doesn't block clones
	if khData, err := os.ReadFile(filepath.Join(sshDir, "known_hosts")); err == nil && len(khData) > 0 {
		lines = append(lines,
			fmt.Sprintf("printf '%%s' %s | base64 -d > ~/.ssh/known_hosts", shellQuote(base64.StdEncoding.EncodeToString(khData))),
			"chmod 644 ~/.ssh/known_hosts",
		)
	}

	// Sync ~/.ssh/config for custom host entries (e.g. git.unturf.com)
	if cfgData, err := os.ReadFile(filepath.Join(sshDir, "config")); err == nil && len(cfgData) > 0 {
		lines = append(lines,
			fmt.Sprintf("printf '%%s' %s | base64 -d > ~/.ssh/config", shellQuote(base64.StdEncoding.EncodeToString(cfgData))),
			"chmod 600 ~/.ssh/config",
		)
	}

	return strings.Join(lines, "\n"), nil
}

// shellQuote wraps a value in single quotes, escaping embedded single quotes.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
