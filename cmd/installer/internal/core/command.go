package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// commandCancelGrace bounds how long a cancelled command may keep the
// installer waiting on its output before it is abandoned.
const commandCancelGrace = 5 * time.Second

type CommandRunner interface {
	Run(context.Context, string, string, ...string) error
}

type ExecRunner struct {
	Output io.Writer
}

func (r ExecRunner) Run(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = r.Output
	cmd.Stderr = r.Output
	cmd.Env = filteredComposeEnvironment(os.Environ())
	// `docker` runs `compose` as a plugin in a separate process that inherits
	// this output pipe. CommandContext only signals the direct child, so the
	// plugin would keep running and holding the pipe open, and Wait would block
	// on it forever.
	cancelProcessTree(cmd)
	// Backstop for anything the cancellation does not reach: abandon its output
	// rather than hang the installer. Go escalates to SIGKILL once this elapses
	// and closes the pipes, so Wait always returns.
	cmd.WaitDelay = commandCancelGrace
	runErr := cmd.Run()
	if flusher, ok := r.Output.(interface{ Flush() }); ok {
		flusher.Flush()
	}
	if runErr != nil {
		return fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), runErr)
	}
	return nil
}

func filteredComposeEnvironment(environment []string) []string {
	blocked := map[string]bool{
		"COMPOSE_FILE": true, "COMPOSE_PATH_SEPARATOR": true,
		"COMPOSE_ENV_FILES": true, "COMPOSE_DISABLE_ENV_FILE": true,
		"COMPOSE_PROFILES": true, "COMPOSE_PROJECT_NAME": true,
	}
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[key] {
			result = append(result, item)
		}
	}
	return result
}
