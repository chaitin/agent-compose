//go:build linux && cgo && boxlitecgo

package driver

import (
	"os"
	"path/filepath"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func sandboxInitHostSocket(config *appconfig.Config, sandboxID string) string {
	return filepath.Join(sandboxInitHostDir(config, sandboxID), "init.sock")
}

func ensureSandboxInitHostDir(config *appconfig.Config, sandboxID string) (string, error) {
	dir := sandboxInitHostDir(config, sandboxID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
