package schedulers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SandboxDirResolver resolves a sandbox ID to its host-side state directory.
// *sandboxstore.Store satisfies this via its existing SandboxDir method; it is
// declared narrowly here so ResolveEventMessage can be exercised in tests
// without a full sandbox store.
type SandboxDirResolver interface {
	SandboxDir(id string) string
}

// schedulerEventMessageReplacement replaces each run of bytes in an event
// message that is not valid UTF-8. A message travels in a protobuf string
// field, which protobuf rejects while encoding, so one raw byte would otherwise
// fail the whole list/stream response instead of degrading to a replacement
// character. Command output is captured as raw subprocess bytes (the guest
// runtime writes output.txt/stdout.txt/stderr.txt verbatim) and the message
// column stores a plain string, so the sanitizing belongs on the read path that
// produces the display text.
const schedulerEventMessageReplacement = "\uFFFD"

// ResolveEventMessage returns the display text for a scheduler_event and
// whether that text came from a live artifact read. The returned text is always
// valid UTF-8: invalid byte runs are replaced with U+FFFD (see
// schedulerEventMessageReplacement), because the text is sent in a protobuf
// string field that cannot carry arbitrary bytes.
//
// For (loader|scheduler).command.completed events, event.Message is the
// discriminator between old and new rows: rows written before this event
// type started clearing its message (see §4) still carry the full text
// there, so a non-empty message is returned as-is with no I/O — the DB and
// the sandbox cell artifact were written from the same in-memory result at
// the same time, so there is nothing for a fresh artifact read to correct.
// Only a genuinely empty message (a row written after §4) triggers an
// artifact read; if that also fails (sandbox archived, artifact never
// written, or the event has no linked sandbox/cell) it synthesizes a
// "content unavailable" placeholder from the payload's outputBytes.
//
// Every other event type returns event.Message as-is with no I/O.
func ResolveEventMessage(ctx context.Context, event domain.SchedulerEvent, sandboxDirs SandboxDirResolver) (text string, artifactAvailable bool) {
	if !EventMessageNeedsArtifactRead(event) {
		return strings.ToValidUTF8(event.Message, schedulerEventMessageReplacement), true
	}
	if text, ok := readCommandCompletedArtifact(ctx, event, sandboxDirs); ok {
		return strings.ToValidUTF8(text, schedulerEventMessageReplacement), true
	}
	return commandArtifactUnavailableMessage(event), false
}

// EventMessageNeedsArtifactRead reports whether ResolveEventMessage would
// need to read the sandbox cell artifact for this event, i.e. whether it's
// worth scheduling onto a bounded worker pool rather than resolving inline.
// Every non-command.completed event and every command.completed event with
// a non-empty message (see ResolveEventMessage's doc comment) is a cheap,
// I/O-free lookup — only a command.completed row with an empty message
// (written after §4) needs the read.
func EventMessageNeedsArtifactRead(event domain.SchedulerEvent) bool {
	return isCommandCompletedEventType(event.Type) && event.Message == ""
}

func isCommandCompletedEventType(eventType string) bool {
	switch eventType {
	case "scheduler.command.completed", "loader.command.completed":
		return true
	default:
		return false
	}
}

func readCommandCompletedArtifact(ctx context.Context, event domain.SchedulerEvent, sandboxDirs SandboxDirResolver) (string, bool) {
	if sandboxDirs == nil || ctx.Err() != nil {
		return "", false
	}
	sandboxID := strings.TrimSpace(event.LinkedSandboxID)
	cellID := strings.TrimSpace(event.LinkedCellID)
	if sandboxID == "" || cellID == "" {
		return "", false
	}
	cellDir := filepath.Join(sandboxDirs.SandboxDir(sandboxID), "state", "cells", cellID)

	// output.txt already holds the write path's chosen content in the common
	// case (firstHostNonEmpty below picks it first too, so a non-empty read
	// here always wins regardless of what stdout.txt/stderr.txt contain).
	// Check it before paying for two more reads whose result would just be
	// discarded.
	output, outputErr := os.ReadFile(filepath.Join(cellDir, "output.txt"))
	if outputErr == nil && strings.TrimSpace(string(output)) != "" {
		return string(output), true
	}

	stdout, stdoutErr := os.ReadFile(filepath.Join(cellDir, "stdout.txt"))
	stderr, stderrErr := os.ReadFile(filepath.Join(cellDir, "stderr.txt"))
	if outputErr != nil && stdoutErr != nil && stderrErr != nil {
		return "", false
	}
	return firstHostNonEmpty(string(output), string(stdout), string(stderr), "scheduler command completed"), true
}

func commandArtifactUnavailableMessage(event domain.SchedulerEvent) string {
	return fmt.Sprintf("content unavailable (artifact not accessible, %d bytes)", commandEventOutputBytes(event))
}

func commandEventOutputBytes(event domain.SchedulerEvent) int {
	if event.PayloadJSON == "" {
		return 0
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		return 0
	}
	value, ok := payload["outputBytes"].(float64)
	if !ok {
		return 0
	}
	return int(value)
}
