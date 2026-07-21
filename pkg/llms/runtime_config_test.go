package llms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

func TestWriteCodexRuntimeConfigRendersRetryPolicy(t *testing.T) {
	root := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "workspace")}}
	policy := CodexRuntimePolicy{RequestMaxRetries: 0, StreamMaxRetries: 2, StreamIdleTimeout: 1500 * time.Millisecond}
	if err := WriteCodexRuntimeConfig(sandbox, "gpt-test", "http://runtime/openai/v1/", APIProtocolResponses, policy); err != nil {
		t.Fatalf("WriteCodexRuntimeConfig returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(sandbox), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex runtime config: %v", err)
	}
	config := string(data)
	for _, want := range []string{
		`request_max_retries = 0`,
		`stream_max_retries = 2`,
		`stream_idle_timeout_ms = 1500`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex runtime config %q does not contain %q", config, want)
		}
	}
}

func TestCodexRuntimePolicyFromConfigUsesDefaultsAndBoundsValues(t *testing.T) {
	defaults := CodexRuntimePolicyFromConfig(nil)
	if defaults.RequestMaxRetries != 1 || defaults.StreamMaxRetries != 1 || defaults.StreamIdleTimeout != time.Minute {
		t.Fatalf("default Codex runtime policy = %#v", defaults)
	}
	bounded := CodexRuntimePolicyFromConfig(&appconfig.Config{
		CodexRequestMaxRetries: 101,
		CodexStreamMaxRetries:  200,
		LLMTimeout:             3 * time.Second,
	})
	if bounded.RequestMaxRetries != 100 || bounded.StreamMaxRetries != 100 || bounded.StreamIdleTimeout != 3*time.Second {
		t.Fatalf("bounded Codex runtime policy = %#v", bounded)
	}
}
