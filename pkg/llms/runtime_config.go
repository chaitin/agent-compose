package llms

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

const (
	codexManagedMCPStart = "# agent-compose managed mcp start"
	codexManagedMCPEnd   = "# agent-compose managed mcp end"
)

func WriteCodexRuntimeConfig(session *domain.Sandbox, model, baseURL, wireAPI string) error {
	if session == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if model == "" || baseURL == "" {
		return nil
	}
	path := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}
	payload := fmt.Sprintf(`model_provider = "agent_compose"
model = %q

[model_providers.agent_compose]
name = "agent-compose"
base_url = %q
env_key = "AGENT_COMPOSE_SANDBOX_TOKEN"
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

func WriteCodexMCPConfig(session *domain.Sandbox, mcps map[string]compose.NormalizedMCPServerSpec) error {
	if session == nil {
		return nil
	}
	path := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}
	existing := []byte{}
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	}
	managed := buildCodexManagedMCPBlock(mcps)
	merged := replaceManagedTextBlock(string(existing), codexManagedMCPStart, codexManagedMCPEnd, managed)
	if strings.TrimSpace(merged) == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove codex mcp config: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		return fmt.Errorf("write codex mcp config: %w", err)
	}
	return nil
}

func buildCodexManagedMCPBlock(mcps map[string]compose.NormalizedMCPServerSpec) string {
	if len(mcps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(codexManagedMCPStart)
	b.WriteByte('\n')
	keys := make([]string, 0, len(mcps))
	for key := range mcps {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, name := range keys {
		mcp := mcps[name]
		fmt.Fprintf(&b, "\n[mcp_servers.%s]\n", name)
		if mcp.Type == "local" {
			fmt.Fprintf(&b, "command = %q\n", mcp.Command)
			if len(mcp.Args) > 0 {
				args, _ := json.Marshal(mcp.Args)
				fmt.Fprintf(&b, "args = %s\n", args)
			}
			if len(mcp.Env) > 0 {
				b.WriteString("[mcp_servers." + name + ".env]\n")
				envKeys := make([]string, 0, len(mcp.Env))
				for key := range mcp.Env {
					envKeys = append(envKeys, key)
				}
				slices.Sort(envKeys)
				for _, key := range envKeys {
					fmt.Fprintf(&b, "%s = %q\n", key, mcp.Env[key].Value)
				}
			}
		} else {
			fmt.Fprintf(&b, "url = %q\n", mcp.URL)
			if len(mcp.Headers) > 0 {
				b.WriteString("[mcp_servers." + name + ".http_headers]\n")
				headerKeys := make([]string, 0, len(mcp.Headers))
				for key := range mcp.Headers {
					headerKeys = append(headerKeys, key)
				}
				slices.Sort(headerKeys)
				for _, key := range headerKeys {
					fmt.Fprintf(&b, "%s = %q\n", key, mcp.Headers[key].Value)
				}
			}
		}
	}
	b.WriteString(codexManagedMCPEnd)
	b.WriteByte('\n')
	return b.String()
}

func WriteOpenCodeRuntimeConfig(session *domain.Sandbox, providerID, model, baseURL string) error {
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
	path := filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json")
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
					"apiKey":  "{env:AGENT_COMPOSE_SANDBOX_TOKEN}",
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

func WriteOpenCodeMCPConfig(session *domain.Sandbox, mcps map[string]compose.NormalizedMCPServerSpec) error {
	if session == nil {
		return nil
	}
	path := filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create opencode config dir: %w", err)
	}
	payload := map[string]any{}
	if existing, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(existing, &payload)
	}
	mcp := map[string]any{}
	for name, server := range mcps {
		if server.Type == "local" {
			command := append([]string{server.Command}, server.Args...)
			env := map[string]string{}
			for key, value := range server.Env {
				env[key] = value.Value
			}
			mcp[name] = map[string]any{"type": "local", "command": command, "environment": env}
		} else {
			headers := map[string]string{}
			for key, value := range server.Headers {
				headers[key] = value.Value
			}
			mcp[name] = map[string]any{"type": "remote", "url": server.URL, "headers": headers}
		}
	}
	if len(mcp) == 0 {
		delete(payload, "mcp")
	} else {
		payload["mcp"] = mcp
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode opencode mcp config: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write opencode mcp config: %w", err)
	}
	return nil
}

func replaceManagedTextBlock(existing, startMarker, endMarker, managed string) string {
	start := strings.Index(existing, startMarker)
	if start >= 0 {
		end := strings.Index(existing[start:], endMarker)
		if end >= 0 {
			end += start + len(endMarker)
			if end < len(existing) && existing[end] == '\n' {
				end++
			}
			existing = existing[:start] + existing[end:]
		} else {
			existing = existing[:start]
		}
	}
	existing = strings.TrimRight(existing, "\n")
	managed = strings.TrimSpace(managed)
	if managed == "" {
		if existing == "" {
			return ""
		}
		return existing + "\n"
	}
	if existing == "" {
		return managed + "\n"
	}
	return existing + "\n\n" + managed + "\n"
}

func WriteOpenCodeAnthropicRuntimeConfig(session *domain.Sandbox, model, baseURL string) error {
	if session == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if model == "" || baseURL == "" {
		return nil
	}
	path := filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json")
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
					"apiKey":  "{env:AGENT_COMPOSE_SANDBOX_TOKEN}",
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

func GuestRuntimeBaseURL(config *appconfig.Config, session *domain.Sandbox) string {
	if config == nil {
		return ""
	}
	if base := strings.TrimRight(strings.TrimSpace(config.RuntimeBaseURL), "/"); base != "" {
		return base
	}
	if base := strings.TrimRight(strings.TrimSpace(LookupRuntimeBaseURLEnv(session)), "/"); base != "" {
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

func LookupRuntimeBaseURLEnv(session *domain.Sandbox) string {
	if session == nil {
		return ""
	}
	for _, items := range [][]domain.SandboxEnvVar{session.ProviderEnvItems, session.RuntimeEnvItems, session.EnvItems} {
		if value := EnvItemValue(items, RuntimeBaseURLEnvName); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
