package unsandbox

import (
	"encoding/json"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
)

// SSHConfig holds the user's SSH forwarding preferences for unsandbox containers.
type SSHConfig struct {
	// ForwardEnabled controls whether the SSH key is injected into containers.
	// Defaults to true when the config file does not exist.
	ForwardEnabled bool `json:"forward_enabled"`
	// KeyPath is the absolute path to the SSH private key to inject.
	// Empty string means auto-detect from ~/.ssh/ (id_ed25519 → id_ecdsa → id_rsa).
	KeyPath string `json:"key_path"`
}

func sshConfigPath() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return filepath.Join(u.HomeDir, ".orchestra", "ssh-config.json"), nil
}

// LoadSSHConfig reads the persisted SSH config. Returns a default config
// (forward enabled, auto-detect key) if no file exists yet.
func LoadSSHConfig() SSHConfig {
	path, err := sshConfigPath()
	if err != nil {
		return SSHConfig{ForwardEnabled: true}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SSHConfig{ForwardEnabled: true}
	}
	var cfg SSHConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return SSHConfig{ForwardEnabled: true}
	}
	return cfg
}

// SaveSSHConfig persists the SSH config to ~/.orchestra/ssh-config.json.
func SaveSSHConfig(cfg SSHConfig) error {
	path, err := sshConfigPath()
	if err != nil {
		return err
	}

	oldUmask := syscall.Umask(0077)
	defer syscall.Umask(oldUmask)

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

// ListSSHKeys returns the absolute paths of private key files found in ~/.ssh/.
// Looks for id_ed25519, id_ecdsa, and id_rsa.
func ListSSHKeys() []string {
	u, err := user.Current()
	if err != nil {
		return nil
	}
	sshDir := filepath.Join(u.HomeDir, ".ssh")
	candidates := []string{"id_ed25519", "id_ecdsa", "id_rsa"}
	var found []string
	for _, name := range candidates {
		path := filepath.Join(sshDir, name)
		if _, err := os.Stat(path); err == nil {
			found = append(found, path)
		}
	}
	return found
}
