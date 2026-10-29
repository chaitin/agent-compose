//go:build linux && cgo && (microsandboxcgo || boxlitecgo)

package driver

import (
	"os"
	"path/filepath"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// sandboxInitSocketRoot returns the host runtime root shared by all init
// drivers. Docker binds it into the container; VM drivers bind it into the
// guest via sandbox-host.
func sandboxInitSocketRoot(config *appconfig.Config) string {
	if config != nil && strings.TrimSpace(config.SandboxInitSocketDir) != "" {
		return strings.TrimSpace(config.SandboxInitSocketDir)
	}
	return "/run/agent-compose"
}

func sandboxInitHostDir(config *appconfig.Config, sandboxID string) string {
	return filepath.Join(sandboxInitSocketRoot(config), sandboxID)
}

func sandboxInitHostSocket(config *appconfig.Config, sandboxID string) string {
	return filepath.Join(sandboxInitHostDir(config, sandboxID), "init.sock")
}

func sandboxInitBinaryPath(config *appconfig.Config) string {
	if config != nil && strings.TrimSpace(config.SandboxInitBinary) != "" {
		return strings.TrimSpace(config.SandboxInitBinary)
	}
	return appconfig.DefaultSandboxInitBinary
}

func ensureSandboxInitHostDir(config *appconfig.Config, sandboxID string) (string, error) {
	dir := sandboxInitHostDir(config, sandboxID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
