//go:build k8scompose || microsandboxcgo || boxlitecgo

package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// runSandboxInitExec is the driver-neutral buffered/streaming exec adapter.
// Drivers only provide a sandboxinit.Client; the frame-to-ExecResult mapping,
// UTF-8 filtering, and cancellation cleanup stay identical.
func runSandboxInitExec(ctx context.Context, client *sandboxinit.Client, sandboxID, defaultCwd string, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return ExecResult{}, fmt.Errorf("sandbox init exec command is required")
	}
	marker := strings.TrimSpace(spec.Marker)
	if marker == "" {
		marker = newDockerExecMarker()
	}
	session, err := client.Exec(ctx, sandboxinit.Request{
		Command: command,
		Args:    append([]string(nil), spec.Args...),
		Env:     spec.Env,
		Cwd:     firstNonEmpty(spec.Cwd, defaultCwd),
		Marker:  marker,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("start sandbox init exec for sandbox %s: %w", sandboxID, err)
	}
	defer func() { _ = session.Close() }()
	// The driver Exec surface has no stdin input; close the child's stdin like
	// docker exec does, otherwise a command that reads stdin waits forever.
	_ = session.CloseStdin(ctx)

	collector := &dockerExecCollector{stream: stream, filter: newExecOutputFilter()}
	for {
		frame, err := session.Next(ctx)
		if err != nil {
			collector.finish()
			if ctx.Err() != nil {
				killSandboxInitSession(client, session.ID, !spec.KillOnlyOnCancel)
				if !spec.KillOnlyOnCancel {
					// The workload runtime starts the real command in its own
					// process group, so killing the session leader's group is not
					// enough. Kill the marker-tagged tree as well, exactly like
					// the run-level TerminateProcessTree path.
					killSandboxInitTree(client, marker)
				}
				return ExecResult{}, ctx.Err()
			}
			return ExecResult{}, fmt.Errorf("read sandbox init exec %s: %w", session.ID, err)
		}
		switch frame.Type {
		case sandboxinit.FrameStdout:
			collector.writeBytes(frame.Data, StdioStdout)
		case sandboxinit.FrameStderr:
			collector.writeBytes(frame.Data, StdioStderr)
		case sandboxinit.FrameError:
			collector.finish()
			killSandboxInitSession(client, session.ID, true)
			killSandboxInitTree(client, marker)
			return ExecResult{}, fmt.Errorf("sandbox init exec %s: %s", session.ID, frame.Message)
		case sandboxinit.FrameExit:
			collector.finish()
			result := ExecResult{
				ExitCode:      frame.Code,
				Stdout:        collector.stdout.String(),
				Stderr:        collector.stderr.String(),
				Output:        collector.output.String(),
				SecurityFacts: collector.filter.SecurityFacts(),
			}
			result.Success = result.ExitCode == 0
			return result, nil
		}
	}
}

func killSandboxInitSession(client *sandboxinit.Client, sessionID string, tree bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.Kill(ctx, sessionID, tree)
}

// requireSandboxInitExecSuccess turns a completed exec with a non-zero exit
// into an error. runSandboxInitExec returns nil error for any completed process
// so callers that only need a command's effect (bootstrap, signal scripts) must
// inspect the result themselves.
func requireSandboxInitExecSuccess(result ExecResult, operation string) error {
	if result.Success {
		return nil
	}
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Output)
	}
	if detail == "" {
		detail = fmt.Sprintf("exit code %d", result.ExitCode)
	}
	return fmt.Errorf("%s failed: %s", operation, detail)
}
