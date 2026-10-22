package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Detach CLI surface. The guest runtime exposes these subcommands so the
// daemon can run a task behind a guest-side supervisor and re-attach to its
// journal after a daemon restart. See
// docs/design/daemon-upgrade-sandbox-survival.zh-CN.md.
const (
	detachCLI               = "agent-compose-runtime"
	DetachStopReasonUser    = "user"
	DetachStopReasonTimeout = "timeout"
)

// DetachCapabilities is the parsed `agent-compose-runtime capabilities --json`
// output. Unknown fields are ignored so newer guests can add capabilities
// without breaking an older daemon.
type DetachCapabilities struct {
	Detach bool `json:"detach"`
}

// DetachExitReason is the end reason the guest supervisor records for a task.
type DetachExitReason string

const (
	DetachExitCompleted DetachExitReason = "completed"
	DetachExitStopped   DetachExitReason = "stopped"
	DetachExitSignal    DetachExitReason = "signal"
	DetachExitAbnormal  DetachExitReason = "abnormal"
)

// DetachAttachRecord is one NDJSON line emitted by `attach`.
type DetachAttachRecord struct {
	Type       string `json:"t"`
	Stream     string `json:"s,omitempty"`
	Text       string `json:"text,omitempty"`
	Offset     uint64 `json:"offset,omitempty"`
	From       uint64 `json:"from,omitempty"`
	To         uint64 `json:"to,omitempty"`
	Code       int    `json:"code,omitempty"`
	Reason     string `json:"reason,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	Signal     string `json:"signal,omitempty"`
	// Detail carries a free-form cause for an abnormal exit (e.g. a journal
	// write failure), written by the supervisor with the exit record.
	Detail string `json:"detail,omitempty"`
}

// IsExit reports whether the record is the terminal exit record.
func (r DetachAttachRecord) IsExit() bool { return r.Type == "exit" }

// IsOut reports whether the record carries a chunk of task output.
func (r DetachAttachRecord) IsOut() bool { return r.Type == "out" }

// ParseDetachCapabilities parses the single JSON line printed by
// `capabilities --json`. A command that does not exist or prints non-JSON
// yields ok=false so callers fall back to the legacy path without an error.
func ParseDetachCapabilities(output string) (DetachCapabilities, bool, error) {
	line := strings.TrimSpace(output)
	if line == "" {
		return DetachCapabilities{}, false, nil
	}
	var caps DetachCapabilities
	if err := json.Unmarshal([]byte(line), &caps); err != nil {
		return DetachCapabilities{}, false, nil
	}
	return caps, true, nil
}

// ParseDetachAttachLine parses one NDJSON line from `attach` output.
func ParseDetachAttachLine(line string) (DetachAttachRecord, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return DetachAttachRecord{}, errors.New("empty attach record")
	}
	var record DetachAttachRecord
	if err := json.Unmarshal([]byte(line), &record); err != nil {
		return DetachAttachRecord{}, fmt.Errorf("parse attach record: %w", err)
	}
	switch record.Type {
	case "out", "gap", "exit":
	default:
		return DetachAttachRecord{}, fmt.Errorf("unknown attach record type %q", record.Type)
	}
	return record, nil
}

// DetachExitResult is the daemon's interpretation of an attach exit record.
type DetachExitResult struct {
	ExitCode int
	Reason   DetachExitReason
	// StopReason is set when Reason is DetachExitStopped.
	StopReason string
	Signal     string
	// Detail carries the cause for an abnormal exit, when the supervisor
	// recorded one.
	Detail string
}

// DetachExitResultFor maps an exit record to its typed result.
func DetachExitResultFor(record DetachAttachRecord) DetachExitResult {
	return DetachExitResult{
		ExitCode:   record.Code,
		Reason:     DetachExitReason(record.Reason),
		StopReason: record.StopReason,
		Signal:     record.Signal,
		Detail:     record.Detail,
	}
}

// ExecResultFromDetachExit maps a detach exit result to an ExecResult plus the
// error the run completion path should treat the outcome with: context.Canceled
// for a user stop, a concrete error for timeout/abnormal/signal, and nil for a
// completed task (a non-zero exit code then drives the failure transition).
func ExecResultFromDetachExit(exit DetachExitResult) (domain.ExecResult, error) {
	switch exit.Reason {
	case DetachExitStopped:
		result := domain.ExecResult{ExitCode: exit.ExitCode}
		if exit.StopReason == DetachStopReasonUser {
			return result, fmt.Errorf("detached execution stopped by user: %w", context.Canceled)
		}
		return result, fmt.Errorf("detached execution stopped by %s", firstNonEmpty(exit.StopReason, "supervisor"))
	case DetachExitSignal:
		return domain.ExecResult{ExitCode: exit.ExitCode}, fmt.Errorf("detached execution terminated by signal %s", firstNonEmpty(exit.Signal, "unknown"))
	case DetachExitAbnormal:
		detail := firstNonEmpty(exit.Detail, "supervisor gone without an exit record")
		return domain.ExecResult{ExitCode: -1}, fmt.Errorf("detached execution died unmanaged: %s", detail)
	case DetachExitCompleted:
		fallthrough
	default:
		return domain.ExecResult{ExitCode: exit.ExitCode, Success: exit.ExitCode == 0}, nil
	}
}

// BuildDetachCapabilitiesSpec builds `agent-compose-runtime capabilities --json`.
func BuildDetachCapabilitiesSpec(config *appconfig.Config) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	return domain.ExecSpec{
		Command: detachCLI,
		Args:    []string{"capabilities", "--json"},
		Cwd:     config.GuestWorkspacePath,
	}
}

// DetachStartOptions are the supervisor settings for one detached task.
type DetachStartOptions struct {
	// TaskTimeout, when positive, is handed to the supervisor, which then
	// stops the task with reason "timeout" by itself, so the run timeout holds
	// while no daemon is attached. It is relative because guest and host
	// clocks can differ.
	TaskTimeout time.Duration
	// Stdin gives the task a stdin fed through `attach --stdin`.
	Stdin bool
	// RawCommand runs taskArgs as a command line (argv[0] first) instead of
	// as an agent-compose-runtime subcommand.
	RawCommand bool
}

// BuildDetachStartSpec builds the `start --detach` exec. taskArgs is the guest
// CLI subcommand plus its flags (e.g. ["exec", "--request-file", ...]); the
// supervisor re-enters the CLI with those args, so no `agent-compose-runtime`
// prefix belongs here. The returned spec carries the execution id as its
// marker so a later TerminateProcessTree can reach the whole detached tree.
func BuildDetachStartSpec(config *appconfig.Config, executionID string, opts DetachStartOptions, taskArgs []string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	startArgs := []string{
		detachCLI, "start", "--detach",
		"--execution-id", executionID,
		"--state-root", config.GuestStateRoot,
	}
	if ms := opts.TaskTimeout.Milliseconds(); ms > 0 {
		startArgs = append(startArgs, "--task-timeout-ms", strconv.FormatInt(ms, 10))
	}
	if opts.Stdin {
		startArgs = append(startArgs, "--stdin")
	}
	if opts.RawCommand {
		startArgs = append(startArgs, "--raw-command")
	}
	startArgs = append(startArgs, "--")
	startArgs = append(startArgs, taskArgs...)
	command := strings.Join([]string{
		"set -e",
		"cd " + ShellQuote(config.GuestWorkspacePath),
		"mkdir -p " + ShellQuote(config.GuestHomePath),
		strings.Join(shellQuoteAll(startArgs), " "),
	}, " && ")
	return domain.ExecSpec{
		Command: "sh",
		Args:    []string{"-lc", command},
		Cwd:     config.GuestWorkspacePath,
		Marker:  executionID,
		// The start exec spawns the detached supervisor and only waits for its
		// ready record. On a daemon shutdown mid-start the driver must kill the
		// exec itself, not the whole marker tree: the marker is the execution id
		// and is inherited by the supervisor and task, which must survive to be
		// re-attached by the next daemon. The marker is still injected so the
		// later TerminateProcessTree fallback can reach the whole tree.
		KillOnlyOnCancel: true,
	}
}

// BuildDetachAttachSpec builds the `attach` exec that replays the journal from
// from and follows it until the task exits. KillOnlyOnCancel is set so a
// daemon shutdown disconnects the follower without touching the task tree.
func BuildDetachAttachSpec(config *appconfig.Config, executionID string, from uint64) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	return domain.ExecSpec{
		Command: detachCLI,
		Args: []string{
			"attach",
			"--execution-id", executionID,
			"--from", fmt.Sprintf("%d", from),
			"--state-root", config.GuestStateRoot,
		},
		Cwd:              config.GuestWorkspacePath,
		KillOnlyOnCancel: true,
	}
}

// BuildDetachStopSpec builds the `stop` exec that asks the supervisor to
// terminate the task with the given reason.
func BuildDetachStopSpec(config *appconfig.Config, executionID, reason string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	return domain.ExecSpec{
		Command: detachCLI,
		Args: []string{
			"stop",
			"--execution-id", executionID,
			"--reason", reason,
			"--state-root", config.GuestStateRoot,
		},
		Cwd: config.GuestWorkspacePath,
	}
}

// BuildDetachCleanupSpec builds the `cleanup` exec that removes a finished
// execution's guest state (journal, ready/exit/stop records). The guest
// refuses while the task is still running.
func BuildDetachCleanupSpec(config *appconfig.Config, executionID string) domain.ExecSpec {
	appconfig.ApplyDefaultGuestPaths(config)
	return domain.ExecSpec{
		Command: detachCLI,
		Args: []string{
			"cleanup",
			"--execution-id", executionID,
			"--state-root", config.GuestStateRoot,
		},
		Cwd: config.GuestWorkspacePath,
	}
}

func shellQuoteAll(values []string) []string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = ShellQuote(value)
	}
	return quoted
}
