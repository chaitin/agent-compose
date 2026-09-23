package llms

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	codexManagedMCPStart = "# agent-compose managed mcp start"
	codexManagedMCPEnd   = "# agent-compose managed mcp end"
)

type CodexRuntimePolicy struct {
	RequestMaxRetries uint64
	StreamMaxRetries  uint64
	StreamIdleTimeout time.Duration
}

func CodexRuntimePolicyFromConfig(config *appconfig.Config) CodexRuntimePolicy {
	if config == nil {
		return CodexRuntimePolicy{
			RequestMaxRetries: appconfig.DefaultCodexRequestMaxRetries,
			StreamMaxRetries:  appconfig.DefaultCodexStreamMaxRetries,
			StreamIdleTimeout: appconfig.DefaultCodexStreamIdleTimeout,
		}
	}
	idleTimeout := config.CodexStreamIdleTimeout
	if idleTimeout < time.Millisecond {
		idleTimeout = config.LLMTimeout
	}
	if idleTimeout < time.Millisecond {
		idleTimeout = appconfig.DefaultCodexStreamIdleTimeout
	}
	return normalizeCodexRuntimePolicy(CodexRuntimePolicy{
		RequestMaxRetries: min(config.CodexRequestMaxRetries, appconfig.MaxCodexRetries),
		StreamMaxRetries:  min(config.CodexStreamMaxRetries, appconfig.MaxCodexRetries),
		StreamIdleTimeout: idleTimeout,
	})
}

func normalizeCodexRuntimePolicy(policy CodexRuntimePolicy) CodexRuntimePolicy {
	policy.RequestMaxRetries = min(policy.RequestMaxRetries, appconfig.MaxCodexRetries)
	policy.StreamMaxRetries = min(policy.StreamMaxRetries, appconfig.MaxCodexRetries)
	if policy.StreamIdleTimeout < time.Millisecond {
		policy.StreamIdleTimeout = appconfig.DefaultCodexStreamIdleTimeout
	}
	return policy
}

// CodexRuntimeConfig groups WriteCodexRuntimeConfig's model/endpoint/retry inputs.
type CodexRuntimeConfig struct {
	Model   string
	BaseURL string
	WireAPI string
	// CredentialEnv names the environment variable codex reads its API key from.
	// A managed run points at the facade token; a direct run points at the
	// vendor key the dialect writer exports. Empty defaults to the facade token.
	CredentialEnv string
	Policy        CodexRuntimePolicy
}

func WriteCodexRuntimeConfig(session *domain.Sandbox, cfg CodexRuntimeConfig) error {
	if session == nil {
		return nil
	}
	model := strings.TrimSpace(cfg.Model)
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	wireAPI := cfg.WireAPI
	policy := cfg.Policy
	if model == "" || baseURL == "" {
		return nil
	}
	credentialEnv := strings.TrimSpace(cfg.CredentialEnv)
	if credentialEnv == "" {
		credentialEnv = guestFacadeTokenEnvName
	}
	wireAPI = NormalizeWireAPI(wireAPI)
	if wireAPI != APIProtocolResponses {
		return fmt.Errorf("codex model-provider wire API %q is unsupported; expected %q", wireAPI, APIProtocolResponses)
	}
	policy = normalizeCodexRuntimePolicy(policy)
	path := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}
	payload := fmt.Sprintf(`model_provider = "agent_compose"
model = %q
check_for_update_on_startup = false

[model_providers.agent_compose]
name = "agent-compose"
base_url = %q
env_key = %q
wire_api = %q
request_max_retries = %d
stream_max_retries = %d
stream_idle_timeout_ms = %d

# Codex otherwise clones the official curated plugin marketplace on startup, and
# a fresh sandbox has no ~/.codex/plugins cache to hit. Keep in sync with
# assets/.codex/config.toml, which seeds the same defaults for devbox images.
[features]
plugins = false
plugin_hooks = false
remote_plugin = false
plugin_sharing = false

[sandbox_workspace_write]
exclude_tmpdir_env_var = false
exclude_slash_tmp = false
network_access = true

[shell_environment_policy]
inherit = "all"
ignore_default_excludes = false

[history]
persistence = "save-all"
`, model, baseURL, credentialEnv, wireAPI, policy.RequestMaxRetries, policy.StreamMaxRetries, policy.StreamIdleTimeout.Milliseconds())
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		return fmt.Errorf("write codex config: %w", err)
	}
	return nil
}

func WriteCodexMCPConfig(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, mcps map[string]compose.NormalizedMCPServerSpec, writeGuestFile execution.GuestFileWriterFunc) error {
	if session == nil {
		return nil
	}
	path := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create codex config dir: %w", err)
	}
	existing := []byte{}
	hadExisting := false
	if data, err := os.ReadFile(path); err == nil {
		existing = data
		hadExisting = len(strings.TrimSpace(string(data))) > 0
	}
	managed := buildCodexManagedMCPBlock(mcps)
	merged := replaceManagedTextBlock(string(existing), codexManagedMCPStart, codexManagedMCPEnd, managed)
	if strings.TrimSpace(merged) == "" {
		// Deleting the daemon's own copy is enough for docker/boxlite, whose
		// guest sees the same file through the shared mount. A no-shared-mount
		// guest (k8s) has no such link: without also pushing the now-empty
		// content, a sandbox Pod reused across runs would keep whatever MCP
		// config an earlier run pushed here, even after every MCP server has
		// since been removed from the project. Gated on hadExisting so a
		// session that never had anything written here (no managed provider,
		// nothing to clear) doesn't pay for an exec round trip on every call.
		//
		// Push before removing path: hadExisting is derived from path's
		// existence, so if path were removed first and the guest push then
		// failed, a retry would see hadExisting=false and skip clearing the
		// guest forever.
		if hadExisting && writeGuestFile != nil {
			appconfig.ApplyDefaultGuestPaths(config)
			guestPath := filepath.Join(config.GuestHomePath, ".codex", "config.toml")
			if err := writeGuestFile(ctx, guestPath, nil); err != nil {
				return fmt.Errorf("clear codex mcp config on guest: %w", err)
			}
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove codex mcp config: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		return fmt.Errorf("write codex mcp config: %w", err)
	}
	// The merge above always operates on the daemon's own local copy of this
	// file (updated by this same push on every prior call), so pushing the
	// freshly merged result keeps a no-shared-mount guest (k8s) in sync the
	// same way a mount would, regardless of how many times this has run for
	// this sandbox before.
	if writeGuestFile != nil {
		appconfig.ApplyDefaultGuestPaths(config)
		guestPath := filepath.Join(config.GuestHomePath, ".codex", "config.toml")
		if err := writeGuestFile(ctx, guestPath, []byte(merged)); err != nil {
			return fmt.Errorf("push codex mcp config to guest: %w", err)
		}
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
				// Args is []string, which encoding/json can always encode, so the
				// error is unreachable and dropping it cannot lose an args line.
				// Should Args ever hold an arbitrary type, omitting the line keeps
				// the generated TOML parseable; emitting a bare "args =" would not.
				if args, argsErr := json.Marshal(mcp.Args); argsErr == nil {
					fmt.Fprintf(&b, "args = %s\n", args)
				}
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

// WriteOpenCodeRuntimeConfig registers the llm facade as OpenCode's provider.
//
// The provider key is always the daemon's own key, never the upstream
// connection id, so renaming a connection cannot change what the guest sees
// and an upstream literally named after a built-in OpenCode provider cannot
// collide with it. inbound selects the AI SDK package, because that package is
// what decides the protocol OpenCode posts to the facade. credentialEnv names
// the environment variable the generated config reads the API key from: the
// facade token for a managed run, the vendor key for a direct one.
func WriteOpenCodeRuntimeConfig(session *domain.Sandbox, inbound Protocol, model, baseURL, credentialEnv string) error {
	if session == nil {
		return nil
	}
	model = strings.TrimSpace(model)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	credentialEnv = strings.TrimSpace(credentialEnv)
	if credentialEnv == "" {
		credentialEnv = guestFacadeTokenEnvName
	}
	if model == "" || baseURL == "" {
		return nil
	}
	providerPackage, err := openCodeProviderPackage(inbound)
	if err != nil {
		return err
	}
	path := filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create opencode config dir: %w", err)
	}
	payload := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			GuestProviderAgentCompose: map[string]any{
				"npm":  providerPackage,
				"name": GuestProviderAgentCompose,
				"options": map[string]any{
					"baseURL": baseURL,
					"apiKey":  "{env:" + credentialEnv + "}",
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
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write opencode config: %w", err)
	}
	return nil
}

// openCodeProviderPackage maps the inbound protocol onto the AI SDK package
// that speaks it. OpenCode cannot speak the Responses API, so a responses
// upstream is converted to chat completions by the daemon before it reaches
// this writer.
func openCodeProviderPackage(inbound Protocol) (string, error) {
	switch inbound {
	case ProtocolChatCompletions:
		return "@ai-sdk/openai-compatible", nil
	case ProtocolMessages:
		return "@ai-sdk/anthropic", nil
	default:
		return "", fmt.Errorf("opencode cannot speak %s to the llm facade", inbound)
	}
}

func WriteOpenCodeMCPConfig(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, mcps map[string]compose.NormalizedMCPServerSpec, writeGuestFile execution.GuestFileWriterFunc) error {
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
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write opencode mcp config: %w", err)
	}
	// Same "always push the daemon's freshly merged local copy" reasoning as
	// WriteCodexMCPConfig above.
	if writeGuestFile != nil {
		appconfig.ApplyDefaultGuestPaths(config)
		guestPath := filepath.Join(config.GuestHomePath, ".config", "opencode", "opencode.json")
		if err := writeGuestFile(ctx, guestPath, data); err != nil {
			return fmt.Errorf("push opencode mcp config to guest: %w", err)
		}
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
