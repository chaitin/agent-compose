package core

import (
	"path/filepath"
	"testing"
)

func TestSelectDataDirPostgresSkipsSQLiteLayoutProbe(t *testing.T) {
	installDir := t.TempDir()
	writeTestFile(t, filepath.Join(installDir, "data", "agent-compose", "data.db"), "legacy", 0o600)
	writeTestFile(t, filepath.Join(installDir, "data", "data.db"), "current", 0o600)
	env := parseEnvFile([]byte("DB_DRIVER=postgres\n"))

	relative, absolute, err := selectDataDir(installDir, env, true)
	if err != nil {
		t.Fatalf("selectDataDir() error = %v", err)
	}
	if relative != "./data" || absolute != filepath.Join(installDir, "data") {
		t.Fatalf("selectDataDir() = (%q, %q), want ./data and %q", relative, absolute, filepath.Join(installDir, "data"))
	}
}

func TestSelectDataDirPostgresHonorsExplicitDataDir(t *testing.T) {
	installDir := t.TempDir()
	env := parseEnvFile([]byte("DB_DRIVER=postgres\nAGENT_COMPOSE_DATA_DIR=./data/agent-compose\n"))

	relative, absolute, err := selectDataDir(installDir, env, true)
	if err != nil {
		t.Fatalf("selectDataDir() error = %v", err)
	}
	if relative != "./data/agent-compose" || absolute != filepath.Join(installDir, "data", "agent-compose") {
		t.Fatalf("selectDataDir() = (%q, %q)", relative, absolute)
	}
}
