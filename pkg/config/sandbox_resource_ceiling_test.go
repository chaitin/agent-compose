package config

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samber/do/v2"
)

func TestSandboxResourceCeilings(t *testing.T) {
	load := func(t *testing.T) (*Config, error) {
		t.Helper()
		t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
		di := do.New()
		do.ProvideValue(di, slog.Default())
		return NewConfig(di)
	}

	t.Run("unset is no ceiling", func(t *testing.T) {
		cfg, err := load(t)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if cfg.SandboxMaxCPUMillis != 0 || cfg.SandboxMaxMemoryMiB != 0 {
			t.Fatalf("ceilings = %d millicpus / %d MiB, want none", cfg.SandboxMaxCPUMillis, cfg.SandboxMaxMemoryMiB)
		}
	})

	t.Run("environment sets the ceilings", func(t *testing.T) {
		t.Setenv("SANDBOX_MAX_CPUS", "2.5")
		t.Setenv("SANDBOX_MAX_MEMORY_MIB", "8192")
		cfg, err := load(t)
		if err != nil {
			t.Fatalf("NewConfig: %v", err)
		}
		if cfg.SandboxMaxCPUMillis != 2500 || cfg.SandboxMaxMemoryMiB != 8192 {
			t.Fatalf("ceilings = %d millicpus / %d MiB, want 2500 / 8192", cfg.SandboxMaxCPUMillis, cfg.SandboxMaxMemoryMiB)
		}
	})

	// A ceiling is a safety bound: a typo must fail startup, not silently lift it.
	for name, value := range map[string]string{
		"SANDBOX_MAX_CPUS":       "eight",
		"SANDBOX_MAX_MEMORY_MIB": "0",
	} {
		t.Run("malformed "+name+" fails startup", func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := load(t); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("NewConfig error = %v, want a startup failure naming %s", err, name)
			}
		})
	}
}
