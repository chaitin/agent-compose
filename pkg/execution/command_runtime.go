package execution

import (
	"context"
	"fmt"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"os"
	"path/filepath"
	"strings"
)

const DefaultSchedulerCommandMaxOutputBytes = int64(1024 * 1024)

type RuntimeCommandRequest struct {
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

// RuntimeCommandSpec describes a command to run, independent of where its
// artifacts land. It intentionally has no ArtifactDir field: that value is
// always the caller's guest execution/cell state dir and is supplied
// separately to RuntimeCommandRequestPayloadFromCommand, so it can't be
// silently dropped if a caller reuses a RuntimeCommandRequest as input.
type RuntimeCommandSpec struct {
	Mode           string
	Command        string
	Args           []string
	Script         string
	Cwd            string
	Env            map[string]string
	TimeoutMs      int64
	MaxOutputBytes int64
}

func RuntimeCommandRequestPayload(config *appconfig.Config, request domain.SchedulerCommandRequest, guestCellDir string) RuntimeCommandRequest {
	return RuntimeCommandRequestPayloadFromCommand(config, RuntimeCommandSpec{
		Mode:           request.Mode,
		Command:        request.Command,
		Args:           request.Args,
		Script:         request.Script,
		Cwd:            request.Cwd,
		Env:            request.Env,
		TimeoutMs:      request.TimeoutMs,
		MaxOutputBytes: request.MaxOutputBytes,
	}, guestCellDir)
}

// RuntimeCommandRequestPayloadFromCommand normalizes spec's fields (mode
// lowercased, cwd defaulted, max output bytes defaulted) and stamps
// guestArtifactDir onto the result.
func RuntimeCommandRequestPayloadFromCommand(config *appconfig.Config, spec RuntimeCommandSpec, guestArtifactDir string) RuntimeCommandRequest {
	appconfig.ApplyDefaultGuestPaths(config)
	maxOutputBytes := spec.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultSchedulerCommandMaxOutputBytes
	}
	cwd := strings.TrimSpace(spec.Cwd)
	if cwd == "" {
		cwd = config.GuestWorkspacePath
	}
	return RuntimeCommandRequest{
		Mode:           strings.ToLower(strings.TrimSpace(spec.Mode)),
		Command:        spec.Command,
		Args:           append([]string(nil), spec.Args...),
		Script:         spec.Script,
		Cwd:            cwd,
		Env:            spec.Env,
		TimeoutMs:      spec.TimeoutMs,
		MaxOutputBytes: maxOutputBytes,
		ArtifactDir:    guestArtifactDir,
	}
}

func BuildSchedulerCommandExecSpec(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, guestRequestPath, home string) domain.ExecSpec {
	return BuildRuntimeCommandExecSpec(ctx, config, session, guestRequestPath, home)
}

func BuildRuntimeCommandExecSpec(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, guestRequestPath, home string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	env := BuildSandboxExecEnv(ctx, config, session, home)
	command := strings.Join([]string{
		"set -e",
		"cd " + ShellQuote(config.GuestWorkspacePath),
		"mkdir -p " + ShellQuote(home),
		"agent-compose-runtime exec" +
			" --request-file " + ShellQuote(guestRequestPath) +
			" --state-root " + ShellQuote(config.GuestStateRoot) +
			" --workspace " + ShellQuote(config.GuestWorkspacePath) +
			" --home " + ShellQuote(home),
	}, " && ")
	return domain.ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Env:     env,
		Cwd:     config.GuestWorkspacePath,
	}
}

func RuntimeCommandResultToExecResult(result domain.RuntimeCommandResult) domain.ExecResult {
	return domain.ExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Output:   result.Output,
		ExitCode: result.ExitCode,
		Success:  result.Success,
	}
}

func BuildSandboxExecEnv(ctx context.Context, config *appconfig.Config, session *domain.Sandbox, home string) map[string]string {
	appconfig.ApplyDefaultGuestPaths(config)
	env := runtimeEnvMap(session.EnvItems)
	if env == nil {
		env = map[string]string{}
	}
	for key, value := range managedRuntimeEnvMap(session.RuntimeEnvItems) {
		env[key] = value
	}
	if base := strings.TrimRight(strings.TrimSpace(config.RuntimeBaseURL), "/"); base != "" {
		env["AGENT_COMPOSE_RUNTIME_BASE_URL"] = base
	}
	env["GOPATH"] = "/usr/local/go"
	env["PATH"] = "/root/.local/bin:/usr/local/go/bin:/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	env["SANDBOX_ID"] = session.Summary.ID
	env["WORKSPACE"] = config.GuestWorkspacePath
	env["STATE_ROOT"] = config.GuestStateRoot
	env["RUNTIME_ROOT"] = config.GuestRuntimeRoot
	env["VERSION"] = config.Version
	ApplyAgentTelemetryEnv(ctx, config, session, env)
	return env
}

func runtimeEnvMap(items []domain.SandboxEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" || driverpkg.LLMProviderEnvName(name) {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func managedRuntimeEnvMap(items []domain.SandboxEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
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

func MirrorRuntimeCommandArtifacts(hostCellDir string, result domain.RuntimeCommandResult) error {
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
	// command-result.json is written by the guest runtime directly into the
	// shared cell dir; host-side rewrites can fail under mixed host/guest users.
	return nil
}
