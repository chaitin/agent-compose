package exec

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	commandResultPrefix               = "__COMMAND_RESULT__"
	llmFacadeTokenSourceLoaderCommand = "loader_command"
)

const CommandResultPrefix = commandResultPrefix

type runtimeCommandRequestJSON struct {
	Mode           string            `json:"mode"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Script         string            `json:"script,omitempty"`
	Cwd            string            `json:"cwd"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMs      int64             `json:"timeoutMs,omitempty"`
	MaxOutputBytes int64             `json:"maxOutputBytes"`
	ArtifactDir    string            `json:"artifactDir"`
}

func validateLoaderCommandRequest(request LoaderCommandRequest) error {
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	switch mode {
	case "exec":
		if strings.TrimSpace(request.Command) == "" {
			return fmt.Errorf("loader command is required")
		}
	case "shell":
		if strings.TrimSpace(request.Script) == "" {
			return fmt.Errorf("loader shell script is required")
		}
	default:
		return fmt.Errorf("loader command mode must be exec or shell")
	}
	if request.TimeoutMs < 0 {
		return fmt.Errorf("loader command timeoutMs must be non-negative")
	}
	if request.MaxOutputBytes < 0 {
		return fmt.Errorf("loader command maxOutputBytes must be non-negative")
	}
	return nil
}

func loaderCommandContext(ctx context.Context, timeoutMs int64) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}

func loaderCommandCellSource(request LoaderCommandRequest) string {
	if strings.EqualFold(strings.TrimSpace(request.Mode), "shell") {
		return request.Script
	}
	items := append([]string{request.Command}, request.Args...)
	return strings.Join(items, " ")
}

func runtimeCommandRequestPayload(config *appconfig.Config, request LoaderCommandRequest, guestCellDir string) runtimeCommandRequestJSON {
	appconfig.ApplyDefaultGuestPaths(config)
	maxOutputBytes := request.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = defaultLoaderCommandMaxOutputBytes
	}
	cwd := strings.TrimSpace(request.Cwd)
	if cwd == "" {
		cwd = config.GuestWorkspacePath
	}
	return runtimeCommandRequestJSON{
		Mode:           strings.ToLower(strings.TrimSpace(request.Mode)),
		Command:        request.Command,
		Args:           append([]string(nil), request.Args...),
		Script:         request.Script,
		Cwd:            cwd,
		Env:            request.Env,
		TimeoutMs:      request.TimeoutMs,
		MaxOutputBytes: maxOutputBytes,
		ArtifactDir:    guestCellDir,
	}
}

func buildLoaderCommandExecSpec(config *appconfig.Config, session *Session, guestRequestPath string) ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	commandHome := guestSessionHome(config)
	env := buildSessionExecEnv(config, session, commandHome)
	command := strings.Join([]string{
		"set -e",
		"cd " + shellQuote(config.GuestWorkspacePath),
		"mkdir -p " + shellQuote(commandHome),
		"agent-compose-runtime exec" +
			" --request-file " + shellQuote(guestRequestPath) +
			" --state-root " + shellQuote(config.GuestStateRoot) +
			" --workspace " + shellQuote(config.GuestWorkspacePath) +
			" --home " + shellQuote(commandHome),
	}, " && ")
	return ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}

func buildSessionExecEnv(config *appconfig.Config, session *Session, home string) map[string]string {
	appconfig.ApplyDefaultGuestPaths(config)
	env := runtimeEnvMap(session.EnvItems)
	if env == nil {
		env = map[string]string{}
	}
	for key, value := range managedRuntimeEnvMap(session.RuntimeEnvItems) {
		env[key] = value
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SESSION_ID"] = session.Summary.ID
	env["WORKSPACE"] = config.GuestWorkspacePath
	env["STATE_ROOT"] = config.GuestStateRoot
	env["RUNTIME_ROOT"] = config.GuestRuntimeRoot
	env["VERSION"] = config.Version
	_ = home
	return env
}

func runtimeEnvMap(items []SessionEnvVar) map[string]string {
	if len(items) == 0 {
		return nil
	}
	env := map[string]string{}
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func RuntimeEnvMap(items []SessionEnvVar) map[string]string {
	return runtimeEnvMap(items)
}

func managedRuntimeEnvMap(items []SessionEnvVar) map[string]string {
	return runtimeEnvMap(items)
}

func ManagedRuntimeEnvMap(items []SessionEnvVar) map[string]string {
	return managedRuntimeEnvMap(items)
}

func findCommandExecPayload(raw string) (RuntimeCommandResult, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, commandResultPrefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, commandResultPrefix))
		}
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var payload RuntimeCommandResult
		if json.Unmarshal([]byte(line), &payload) == nil {
			return payload, true
		}
	}
	return RuntimeCommandResult{}, false
}

func parseCommandExecResult(result ExecResult) (RuntimeCommandResult, error) {
	raw := firstNonEmpty(result.Stdout, result.Output)
	if strings.TrimSpace(raw) == "" {
		return RuntimeCommandResult{}, fmt.Errorf("decode command result: empty stdout")
	}
	payload, ok := findCommandExecPayload(raw)
	if !ok && strings.TrimSpace(result.Output) != strings.TrimSpace(raw) {
		payload, ok = findCommandExecPayload(result.Output)
	}
	if !ok {
		return RuntimeCommandResult{}, fmt.Errorf("decode command result: no result payload found")
	}
	return payload, nil
}

func mirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	files := map[string]string{
		"stdout.txt": result.Stdout,
		"stderr.txt": result.Stderr,
		"output.txt": result.Output,
	}
	for name, content := range files {
		path := filepath.Join(hostCellDir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write command artifact %s: %w", name, err)
		}
	}
	return nil
}

func MirrorRuntimeCommandArtifacts(hostCellDir string, result RuntimeCommandResult) error {
	return mirrorRuntimeCommandArtifacts(hostCellDir, result)
}

func envItemsFromMap(values map[string]string, secret bool) []SessionEnvVar {
	if len(values) == 0 {
		return nil
	}
	items := make([]SessionEnvVar, 0, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		items = append(items, SessionEnvVar{Name: key, Value: value, Secret: secret})
	}
	return items
}

func normalizeAgentKind(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "", "codex":
		return "codex"
	case "claude":
		return "claude"
	case "gemini":
		return "gemini"
	case "opencode":
		return "opencode"
	default:
		return strings.ToLower(strings.TrimSpace(agent))
	}
}

func mergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	if len(base) == 0 && len(managed) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(managed))
	for key, value := range base {
		if driverpkg.LLMProviderKeyName(key) {
			continue
		}
		result[key] = value
	}
	for key, value := range managed {
		result[key] = value
	}
	return result
}
