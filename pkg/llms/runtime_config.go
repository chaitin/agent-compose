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

func WriteCodexRuntimeConfig(session *domain.Session, model, baseURL, wireAPI string) error {
	if session == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if model == "" || baseURL == "" {
		return nil
	}
	path := filepath.Join(execution.HostSessionHome(session), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}
	payload := fmt.Sprintf(`model_provider = "agent_compose"
model = %q

[model_providers.agent_compose]
name = "agent-compose"
base_url = %q
env_key = "AGENT_COMPOSE_SESSION_TOKEN"
wire_api = %q
request_max_retries = 30
stream_max_retries = 50
stream_idle_timeout_ms = 120000

[sandbox_workspace_write]
exclude_tmpdir_env_var = false
exclude_slash_tmp = false
network_access = true

[shell_environment_policy]
inherit = "all"
ignore_default_excludes = false

[history]
persistence = "save-all"
`, model, baseURL, NormalizeWireAPI(wireAPI))
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("write codex config: %w", err)
	}
	return nil
}

func WriteOpenCodeRuntimeConfig(session *domain.Session, providerID, model, baseURL string) error {
	if session == nil {
		return nil
	}
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if providerID == "" || model == "" || baseURL == "" {
		return nil
	}
	providerPackage := "@ai-sdk/openai-compatible"
	if providerID == "openai" {
		providerPackage = "@ai-sdk/openai"
	}
	path := filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create opencode config dir: %w", err)
	}
	payload := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			providerID: map[string]any{
				"npm":  providerPackage,
				"name": "agent-compose " + providerID,
				"options": map[string]any{
					"baseURL": baseURL,
					"apiKey":  "{env:AGENT_COMPOSE_SESSION_TOKEN}",
				},
				"models": map[string]any{
					model: map[string]any{"name": model},
				},
			},
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode opencode config: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}
	return nil
}

func WriteOpenCodeAnthropicRuntimeConfig(session *domain.Session, model, baseURL string) error {
	if session == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if model == "" || baseURL == "" {
		return nil
	}
	path := filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create opencode config dir: %w", err)
	}
	payload := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"anthropic": map[string]any{
				"npm":  "@ai-sdk/anthropic",
				"name": "agent-compose anthropic",
				"options": map[string]any{
					"baseURL": baseURL,
					"apiKey":  "{env:AGENT_COMPOSE_SESSION_TOKEN}",
				},
				"models": map[string]any{
					model: map[string]any{"name": model},
				},
			},
		},
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode opencode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}
	return nil
}

func GuestOpenCodeConfigPath(config *appconfig.Config) string {
	appconfig.ApplyDefaultGuestPaths(config)
	return filepath.Join(config.GuestHomePath, ".config", "opencode", "opencode.json")
}

func GuestRuntimeBaseURL(config *appconfig.Config, session *domain.Session) string {
	if config == nil {
		return ""
	}
	if base := strings.TrimRight(strings.TrimSpace(LookupRuntimeBaseURLEnv(session)), "/"); base != "" {
		return base
	}
	if base := strings.TrimRight(strings.TrimSpace(config.RuntimeBaseURL), "/"); base != "" {
		return base
	}
	listen := strings.TrimSpace(config.HttpListen)
	if listen == "" {
		return ""
	}
	host, port, ok := strings.Cut(listen, ":")
	if !ok {
		return ""
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	if session != nil && strings.EqualFold(session.Summary.Driver, "docker") && (host == "127.0.0.1" || host == "localhost") {
		return ""
	}
	return "http://" + host + ":" + port
}

func LookupRuntimeBaseURLEnv(session *domain.Session) string {
	if session == nil {
		return ""
	}
	for _, items := range [][]domain.SessionEnvVar{session.ProviderEnvItems, session.RuntimeEnvItems, session.EnvItems} {
		if value := EnvItemValue(items, "AGENT_COMPOSE_RUNTIME_BASE_URL"); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
