package llms

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

const piFacadeProviderID = "agent-compose"

func GuestPiAgentDir(config *appconfig.Config) string {
	appconfig.ApplyDefaultGuestPaths(config)
	return filepath.Join(config.GuestHomePath, ".pi", "agent")
}

// WritePiRuntimeConfig atomically replaces agent-compose's Pi model catalog.
// The API key remains an environment reference so no facade token is persisted.
func WritePiRuntimeConfig(sandbox *domain.Sandbox, model, baseURL, api string) error {
	if sandbox == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	api = strings.TrimSpace(api)
	if model == "" || baseURL == "" || api == "" {
		return nil
	}
	payload := map[string]any{"providers": map[string]any{
		piFacadeProviderID: map[string]any{
			"baseUrl": baseURL,
			"apiKey":  "$AGENT_COMPOSE_SANDBOX_TOKEN",
			"api":     api,
			"models": []map[string]any{{
				"id": model, "name": model, "contextWindow": 128000, "maxTokens": 16384,
			}},
		},
	}}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pi runtime config: %w", err)
	}
	dir := filepath.Join(execution.HostSandboxHome(sandbox), ".pi", "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create pi config dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure pi config dir: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".models-*.json")
	if err != nil {
		return fmt.Errorf("create pi runtime config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure pi runtime config: %w", err)
	}
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write pi runtime config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close pi runtime config: %w", err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(dir, "models.json")); err != nil {
		return fmt.Errorf("replace pi runtime config: %w", err)
	}
	return nil
}
