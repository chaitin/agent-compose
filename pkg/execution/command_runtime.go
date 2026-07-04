package execution

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultLoaderCommandMaxOutputBytes = int64(1024 * 1024)

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

func RuntimeCommandRequestPayload(config *appconfig.Config, request domain.LoaderCommandRequest, guestCellDir string) RuntimeCommandRequest {
	return RuntimeCommandRequestPayloadFromCommand(config, request.Mode, request.Command, request.Args, request.Script, request.Cwd, request.Env, request.TimeoutMs, request.MaxOutputBytes, guestCellDir)
}

func RuntimeCommandRequestPayloadFromCommand(config *appconfig.Config, mode, command string, args []string, script, cwd string, env map[string]string, timeoutMs, maxOutputBytes int64, guestArtifactDir string) RuntimeCommandRequest {
	appconfig.ApplyDefaultGuestPaths(config)
	if maxOutputBytes <= 0 {
		maxOutputBytes = DefaultLoaderCommandMaxOutputBytes
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		cwd = config.GuestWorkspacePath
	}
	return RuntimeCommandRequest{
		Mode:           strings.ToLower(strings.TrimSpace(mode)),
		Command:        command,
		Args:           append([]string(nil), args...),
		Script:         script,
		Cwd:            cwd,
		Env:            env,
		TimeoutMs:      timeoutMs,
		MaxOutputBytes: maxOutputBytes,
		ArtifactDir:    guestArtifactDir,
	}
}

func BuildLoaderCommandExecSpec(config *appconfig.Config, session *domain.Session, guestRequestPath, home string) domain.ExecSpec {
	return BuildRuntimeCommandExecSpec(config, session, guestRequestPath, home)
}

func BuildRuntimeCommandExecSpec(config *appconfig.Config, session *domain.Session, guestRequestPath, home string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	env := BuildSessionExecEnv(config, session, home)
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

func BuildSessionExecEnv(config *appconfig.Config, session *domain.Session, home string) map[string]string {
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
	return env
}

func runtimeEnvMap(items []domain.SessionEnvVar) map[string]string {
	env := make(map[string]string, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		name := strings.TrimSpace(item.Name)
		if name == "" || driverpkg.LLMProviderKeyName(name) {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func managedRuntimeEnvMap(items []domain.SessionEnvVar) map[string]string {
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
