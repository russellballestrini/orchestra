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

// SyncSSHKey reads the local SSH private key and supporting files, and returns
// shell commands to inject them into a container with secure permissions.
// Tries id_ed25519, id_ecdsa, id_rsa in order. Also syncs known_hosts and
// ~/.ssh/config so host verification works without prompts.
// Returns empty string if no key is found (non-fatal).
func SyncSSHKey() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}

	sshDir := filepath.Join(u.HomeDir, ".ssh")

	// Find the first available private key
	keyNames := []string{"id_ed25519", "id_ecdsa", "id_rsa"}
	var keyData []byte
	var keyName string
	for _, name := range keyNames {
		data, err := os.ReadFile(filepath.Join(sshDir, name))
		if err == nil && len(data) > 0 {
			keyData = data
			keyName = name
			break
		}
	}
	if keyData == nil {
		return ""
	}

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

	return strings.Join(lines, "\n")
}

// shellQuote wraps a value in single quotes, escaping embedded single quotes.
func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
