// Package runlog records the complete history of one run as an append-only
// JSON-lines file: what the user sent, what the agent did and answered, what
// the processes printed, and where each turn ended. The database keeps only
// state and a conversation index; everything else lives here.
//
// A log belongs to what it records, not to the sandbox that ran it: a project
// run's log lives in the run's own directory under the daemon data root, and
// a scheduler shell/exec step's in its scheduler run's directory. Removing a
// sandbox leaves both, and the guest cannot reach either.
package runlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the run log inside a run directory.
const FileName = "run.log.jsonl"

// FormatJSONL marks a run whose logs_path points at a run log rather than a
// legacy text transcript.
const FormatJSONL = "jsonl"

type EntryType string

const (
	EntryUser       EntryType = "user"
	EntryText       EntryType = "text"
	EntryToolCall   EntryType = "tool_call"
	EntryToolResult EntryType = "tool_result"
	EntryUsage      EntryType = "usage"
	EntryError      EntryType = "error"
	EntryOutput     EntryType = "output"
	EntryTurnEnd    EntryType = "turn_end"
	EntryTruncated  EntryType = "truncated"
)

// Entry is one line of a run log. Only the fields of its Type are set.
type Entry struct {
	Seq        uint64    `json:"seq"`
	Time       time.Time `json:"ts"`
	Turn       uint32    `json:"turn,omitempty"`
	TextOffset uint64    `json:"text_offset"`
	Type       EntryType `json:"type"`
	// SourceOffset is the end of the guest journal record that produced this entry.
	// It is internal recovery metadata, omitted from transport responses.
	SourceOffset uint64 `json:"src_offset,omitempty"`
	// SourceGap distinguishes journal retention loss from the host log cap.
	SourceGap bool `json:"src_gap,omitempty"`

	// user and text carry Text; error carries its message in Text.
	Text string `json:"text,omitempty"`
	// Initial marks the run's opening prompt, which the text view omits as
	// the legacy transcript did. Later user input is shown, after a newline
	// when Newline says the text before it did not end with one.
	Initial bool `json:"initial,omitempty"`
	Newline bool `json:"nl,omitempty"`

	// output
	Stream string `json:"stream,omitempty"`
	Data   string `json:"data,omitempty"`

	// tool_call and tool_result
	Tool *Tool `json:"tool,omitempty"`

	Usage *Usage `json:"usage,omitempty"`

	// error
	Severity string `json:"severity,omitempty"`
	Code     string `json:"code,omitempty"`

	// turn_end
	StopReason string `json:"stop_reason,omitempty"`
	FinalText  string `json:"final_text,omitempty"`

	// truncated: how much is missing here. For a marker the size cap wrote,
	// the bytes of the text view dropped since the previous marker, which is
	// not the on-disk size the cap counts; for a journal gap (SourceGap), the
	// guest journal bytes lost.
	DroppedBytes uint64 `json:"dropped_bytes,omitempty"`
}

// Tool describes a tool call (when it was issued) or its outcome.
type Tool struct {
	ID       string          `json:"id"`
	ParentID string          `json:"parent_id,omitempty"`
	Name     string          `json:"name,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	Command  string          `json:"command,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Changes  []FileChange    `json:"changes,omitempty"`

	OK          *bool  `json:"ok,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Output      string `json:"output,omitempty"`
	OutputBytes int    `json:"output_bytes,omitempty"`
	Error       string `json:"error,omitempty"`
}

// FileChange identifies a file affected by a tool call.
type FileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type Usage struct {
	Scope            string  `json:"scope,omitempty"`
	Model            string  `json:"model,omitempty"`
	InputTokens      int64   `json:"input_tokens,omitempty"`
	OutputTokens     int64   `json:"output_tokens,omitempty"`
	ReasoningTokens  int64   `json:"reasoning_tokens,omitempty"`
	CachedTokens     int64   `json:"cached_tokens,omitempty"`
	CacheWriteTokens int64   `json:"cache_write_tokens,omitempty"`
	CostUSD          float64 `json:"cost_usd,omitempty"`
}

// IsLogPath reports whether path names a run log (a run's run.log.jsonl or a
// scheduler step's <cell>.log.jsonl) rather than a legacy text transcript,
// which is always a .txt file.
func IsLogPath(path string) bool {
	return strings.HasSuffix(path, ".jsonl")
}

// RunPath is where a project run's log lives under the daemon data root.
// The directory belongs to the run, not to its sandbox, so removing the
// sandbox does not remove the history.
func RunPath(dataRoot, runID string) (string, error) {
	dir, err := RunDir(dataRoot, runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// RunDir is the directory a project run owns under the daemon data root.
func RunDir(dataRoot, runID string) (string, error) {
	if err := checkDataRoot(dataRoot); err != nil {
		return "", err
	}
	runID, err := pathSegment("run id", runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataRoot, "runs", runID), nil
}

// SchedulerCellPath is where one shell/exec step of a scheduler run logs
// its output, inside the directory the scheduler run already owns.
func SchedulerCellPath(dataRoot, schedulerID, schedulerRunID, cellID string) (string, error) {
	if err := checkDataRoot(dataRoot); err != nil {
		return "", err
	}
	segments := []struct{ name, value string }{
		{"scheduler id", schedulerID}, {"scheduler run id", schedulerRunID}, {"cell id", cellID},
	}
	clean := make([]string, len(segments))
	for i, segment := range segments {
		value, err := pathSegment(segment.name, segment.value)
		if err != nil {
			return "", err
		}
		clean[i] = value
	}
	return filepath.Join(dataRoot, "schedulers", clean[0], "runs", clean[1], "cells", clean[2]+".log.jsonl"), nil
}

func checkDataRoot(dataRoot string) error {
	if strings.TrimSpace(dataRoot) == "" {
		return errors.New("run log data root is empty")
	}
	return nil
}

// pathSegment checks that an ID names exactly one directory entry, so a log
// path built from it cannot leave the data root. IDs are generated by the
// daemon, but a log path must not depend on every caller having checked.
func pathSegment(name, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return "", fmt.Errorf("invalid %s %q for a run log path", name, id)
	}
	return id, nil
}

// Render is the entry's contribution to the run's text view. It keeps the
// shape of the legacy transcript: prose as-is, tool calls as a [tool:name]
// header with the command or arguments, then the tool's output.
//
// Render is part of the on-disk format. Each entry stores the text offset its
// rendering starts at, and readers re-render stored entries to serve the text
// view, so changing what Render returns for an existing entry shifts the text
// view of logs already written. testdata/render.golden pins it.
func Render(e Entry) string {
	switch e.Type {
	case EntryUser:
		if e.Initial || strings.TrimSpace(e.Text) == "" {
			return ""
		}
		text := e.Text
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if e.Newline {
			text = "\n" + text
		}
		return text
	case EntryText:
		return e.Text
	case EntryOutput:
		return e.Data
	case EntryToolCall:
		if e.Tool == nil {
			return ""
		}
		return toolCallText(e.Tool)
	case EntryToolResult:
		if e.Tool == nil {
			return ""
		}
		return toolResultText(e.Tool.Output, e.Tool.Error)
	case EntryError:
		if strings.TrimSpace(e.Text) == "" {
			return ""
		}
		return "\n[error] " + strings.TrimSpace(e.Text) + "\n"
	case EntryTruncated:
		return "\n[log truncated]\n"
	default:
		return ""
	}
}

func toolCallText(tool *Tool) string {
	name := strings.TrimSpace(tool.Name)
	if name == "" {
		return ""
	}
	header := "\n[tool:" + name + "]\n"
	if command := strings.TrimSpace(tool.Command); command != "" {
		return header + "$ " + command + "\n"
	}
	if arguments := toolInputText(tool.Input); arguments != "" {
		return header + arguments + "\n"
	}
	if len(tool.Changes) > 0 {
		var text strings.Builder
		text.WriteString(header)
		for _, change := range tool.Changes {
			text.WriteString(change.Kind + " " + change.Path + "\n")
		}
		return text.String()
	}
	return header
}

func toolInputText(input json.RawMessage) string {
	switch trimmed := strings.TrimSpace(string(input)); trimmed {
	case "", "null", "{}", `""`:
		return ""
	default:
		return trimmed
	}
}

func toolResultText(output, failure string) string {
	if failure = strings.TrimSpace(failure); failure == "" {
		return output
	}
	if output != "" && !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return output + "[tool error] " + failure + "\n"
}
