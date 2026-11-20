package schedulers_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

type fakeSandboxDirResolver struct {
	t      *testing.T
	dirs   map[string]string
	forbid bool
}

func (f *fakeSandboxDirResolver) SandboxDir(id string) string {
	if f.forbid {
		f.t.Fatalf("SandboxDir(%q) called for an event type that must not do artifact I/O", id)
	}
	return f.dirs[id]
}

type cellArtifactWrite struct {
	SandboxDir string
	CellID     string
	Name       string
	Content    string
}

func writeCellArtifact(t *testing.T, write cellArtifactWrite) {
	t.Helper()
	cellDir := filepath.Join(write.SandboxDir, "state", "cells", write.CellID)
	if err := os.MkdirAll(cellDir, 0o755); err != nil {
		t.Fatalf("mkdir cell dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cellDir, write.Name), []byte(write.Content), 0o644); err != nil {
		t.Fatalf("write %s: %v", write.Name, err)
	}
}

func TestResolveEventMessageNonCommandTypeReturnsMessageWithoutIO(t *testing.T) {
	event := domain.SchedulerEvent{Type: "scheduler.run.started", Message: "run started"}
	resolver := &fakeSandboxDirResolver{t: t, forbid: true}

	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if text != "run started" || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want %q,true", text, ok, "run started")
	}
}

func TestResolveEventMessageNewRowReadsArtifact(t *testing.T) {
	dir := t.TempDir()
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "output.txt",
		Content:    "full command output",
	})
	resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": dir}}

	event := domain.SchedulerEvent{
		Type:            "scheduler.command.completed",
		Message:         "", // §4: new rows always write an empty message
		LinkedSandboxID: "sandbox-1",
		LinkedCellID:    "cell-1",
	}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if text != "full command output" || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want artifact content,true", text, ok)
	}
}

func TestResolveEventMessageOutputTxtTakesPriorityOverStdoutStderr(t *testing.T) {
	dir := t.TempDir()
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "output.txt",
		Content:    "output.txt wins",
	})
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "stdout.txt",
		Content:    "stdout.txt loses",
	})
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "stderr.txt",
		Content:    "stderr.txt loses",
	})
	resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": dir}}

	event := domain.SchedulerEvent{Type: "scheduler.command.completed", LinkedSandboxID: "sandbox-1", LinkedCellID: "cell-1"}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if text != "output.txt wins" || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want output.txt content to take priority (matches the write-path firstHostNonEmpty order)", text, ok)
	}
}

func TestResolveEventMessageFallsBackToStdoutWhenOutputTxtEmpty(t *testing.T) {
	dir := t.TempDir()
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "output.txt",
	})
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "stdout.txt",
		Content:    "stdout content",
	})
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "stderr.txt",
		Content:    "stderr content",
	})
	resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": dir}}

	event := domain.SchedulerEvent{Type: "scheduler.command.completed", LinkedSandboxID: "sandbox-1", LinkedCellID: "cell-1"}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if text != "stdout content" || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want stdout.txt content when output.txt is empty", text, ok)
	}
}

func TestResolveEventMessageLegacyLoaderTypeReadsArtifact(t *testing.T) {
	dir := t.TempDir()
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "output.txt",
		Content:    "legacy artifact output",
	})
	resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": dir}}

	event := domain.SchedulerEvent{
		Type:            "loader.command.completed",
		Message:         "",
		LinkedSandboxID: "sandbox-1",
		LinkedCellID:    "cell-1",
	}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if text != "legacy artifact output" || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want artifact content,true", text, ok)
	}
}

func TestResolveEventMessageNewRowArtifactUnavailableProducesPlaceholder(t *testing.T) {
	resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": t.TempDir()}} // no cell dir written: sandbox archived / never flushed

	event := domain.SchedulerEvent{
		Type:            "scheduler.command.completed",
		Message:         "",
		PayloadJSON:     `{"outputBytes":4153756}`,
		LinkedSandboxID: "sandbox-1",
		LinkedCellID:    "cell-missing",
	}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if ok {
		t.Fatalf("artifactAvailable = true, want false when artifact cannot be read")
	}
	if !strings.Contains(text, "4153756") {
		t.Fatalf("placeholder text = %q, want it to include outputBytes from payload_json", text)
	}
}

func TestResolveEventMessageHistoricalRowSkipsArtifactReadWhenMessagePresent(t *testing.T) {
	// The DB message and the sandbox cell artifact are written from the same
	// in-memory result at the same time, so a non-empty message means this is
	// a row written before §4 started clearing it — there is nothing for a
	// fresh artifact read to correct. Pin this with a forbidding resolver so
	// the test fails loudly if a future change reintroduces the read: even
	// though a *different* artifact exists on disk (as it never legitimately
	// would in production), it must never be consulted, let alone win.
	dir := t.TempDir()
	writeCellArtifact(t, cellArtifactWrite{
		SandboxDir: dir,
		CellID:     "cell-1",
		Name:       "output.txt",
		Content:    "an artifact must never be consulted here",
	})
	resolver := &fakeSandboxDirResolver{t: t, forbid: true, dirs: map[string]string{"sandbox-1": dir}}

	event := domain.SchedulerEvent{
		Type:            "scheduler.command.completed",
		Message:         "full output written before this event type started clearing message",
		LinkedSandboxID: "sandbox-1",
		LinkedCellID:    "cell-1",
	}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if !ok {
		t.Fatalf("artifactAvailable = false, want true (returned DB message without I/O)")
	}
	if text != event.Message {
		t.Fatalf("text = %q, want historical DB message %q unchanged", text, event.Message)
	}
}

func TestResolveEventMessageMissingLinkedIDsSkipsIOAndFallsBackToMessage(t *testing.T) {
	resolver := &fakeSandboxDirResolver{t: t, forbid: true}

	event := domain.SchedulerEvent{Type: "scheduler.command.completed", Message: "no linked sandbox/cell on this row"}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if text != event.Message || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want %q,true", text, ok, event.Message)
	}
}

func TestEventMessageNeedsArtifactRead(t *testing.T) {
	cases := []struct {
		name  string
		event domain.SchedulerEvent
		want  bool
	}{
		{"non-command type, empty message", domain.SchedulerEvent{Type: "scheduler.run.started", Message: ""}, false},
		{"non-command type, non-empty message", domain.SchedulerEvent{Type: "scheduler.log", Message: "hi"}, false},
		{"command.completed, empty message (new row)", domain.SchedulerEvent{Type: "scheduler.command.completed", Message: ""}, true},
		{"command.completed, non-empty message (historical row)", domain.SchedulerEvent{Type: "scheduler.command.completed", Message: "content"}, false},
		{"legacy loader.command.completed, empty message", domain.SchedulerEvent{Type: "loader.command.completed", Message: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := schedulers.EventMessageNeedsArtifactRead(tc.event); got != tc.want {
				t.Fatalf("EventMessageNeedsArtifactRead(%#v) = %v, want %v", tc.event, got, tc.want)
			}
		})
	}
}

func TestResolveEventMessageNilSandboxDirsFallsBackToMessage(t *testing.T) {
	event := domain.SchedulerEvent{
		Type:            "scheduler.command.completed",
		Message:         "historical content, no resolver wired",
		LinkedSandboxID: "sandbox-1",
		LinkedCellID:    "cell-1",
	}
	text, ok := schedulers.ResolveEventMessage(context.Background(), event, nil)
	if text != event.Message || !ok {
		t.Fatalf("ResolveEventMessage = %q,%v want %q,true", text, ok, event.Message)
	}
}

// command output is captured as raw subprocess bytes, so an artifact read can
// return text that is not valid UTF-8. A scheduler event message travels in a
// protobuf string field, where one bad byte fails the whole response; the
// resolved text must therefore always be valid UTF-8, with each invalid byte
// run replaced by U+FFFD instead of erroring out.
func TestResolveEventMessageSanitizesInvalidUTF8ArtifactBytes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"single invalid byte between markers", "MARKER-BEFORE\n\xe7\nMARKER-AFTER\n", "MARKER-BEFORE\n\uFFFD\nMARKER-AFTER\n"},
		{"truncated multibyte sequence", "a\xe4\xb8z", "a\uFFFDz"},
		{"lone 0xff byte", "a\xffz", "a\uFFFDz"},
		{"run of invalid bytes collapses to one replacement", "\xff\xfe", "\uFFFD"},
		{"valid utf8 is untouched", "héllo 世界", "héllo 世界"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeCellArtifact(t, cellArtifactWrite{SandboxDir: dir, CellID: "cell-1", Name: "output.txt", Content: tc.content})
			resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": dir}}
			event := domain.SchedulerEvent{
				Type:            "scheduler.command.completed",
				Message:         "",
				LinkedSandboxID: "sandbox-1",
				LinkedCellID:    "cell-1",
			}

			text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
			if !ok {
				t.Fatalf("artifactAvailable = false, want true")
			}
			if !utf8.ValidString(text) {
				t.Fatalf("ResolveEventMessage returned invalid UTF-8: %q", text)
			}
			if text != tc.want {
				t.Fatalf("ResolveEventMessage = %q, want %q", text, tc.want)
			}
		})
	}
}

func TestResolveEventMessageSanitizesStdoutFallbackBytes(t *testing.T) {
	dir := t.TempDir()
	writeCellArtifact(t, cellArtifactWrite{SandboxDir: dir, CellID: "cell-1", Name: "output.txt"})
	writeCellArtifact(t, cellArtifactWrite{SandboxDir: dir, CellID: "cell-1", Name: "stdout.txt", Content: "stdout\xe7content"})
	resolver := &fakeSandboxDirResolver{t: t, dirs: map[string]string{"sandbox-1": dir}}
	event := domain.SchedulerEvent{Type: "scheduler.command.completed", Message: "", LinkedSandboxID: "sandbox-1", LinkedCellID: "cell-1"}

	text, ok := schedulers.ResolveEventMessage(context.Background(), event, resolver)
	if !ok {
		t.Fatalf("artifactAvailable = false, want true")
	}
	if want := "stdout\uFFFDcontent"; text != want {
		t.Fatalf("ResolveEventMessage = %q, want %q", text, want)
	}
}

// The message column is inserted as a plain string, so a row written by any
// path (or by an older daemon build) can carry bytes that are not valid UTF-8.
// ResolveEventMessage returns stored messages without any I/O, so the
// sanitizing has to cover that branch too, not just artifact reads.
func TestResolveEventMessageSanitizesStoredMessageWithoutIO(t *testing.T) {
	cases := []struct {
		name  string
		event domain.SchedulerEvent
		want  string
	}{
		{"non-command event", domain.SchedulerEvent{Type: "scheduler.agent.completed", Message: "agent\xe7output"}, "agent\uFFFDoutput"},
		{"historical command.completed row", domain.SchedulerEvent{Type: "scheduler.command.completed", Message: "old\xffrow"}, "old\uFFFDrow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeSandboxDirResolver{t: t, forbid: true}
			text, ok := schedulers.ResolveEventMessage(context.Background(), tc.event, resolver)
			if !ok {
				t.Fatalf("artifactAvailable = false, want true")
			}
			if !utf8.ValidString(text) {
				t.Fatalf("ResolveEventMessage returned invalid UTF-8: %q", text)
			}
			if text != tc.want {
				t.Fatalf("ResolveEventMessage = %q, want %q", text, tc.want)
			}
		})
	}
}

func TestResolveEventMessagePrefersTheStepLogWhichOutlivesTheSandbox(t *testing.T) {
	dataRoot := t.TempDir()
	path := runlog.SchedulerCellPath(dataRoot, "sched-1", "run-1", "cell-1")
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.AppendOutput("stdout", "built\n")
	_ = w.AppendOutput("stderr", "1 warning\n")
	_ = w.Close()
	// The sandbox is gone: its directory does not exist.
	dirs := schedulers.ArtifactDirs{Sandboxes: staticSandboxDirs(filepath.Join(dataRoot, "missing")), DataRoot: dataRoot}
	event := domain.SchedulerEvent{
		SchedulerID: "sched-1", RunID: "run-1", Type: "scheduler.command.completed",
		LinkedSandboxID: "sandbox-1", LinkedCellID: "cell-1",
	}
	text, available := schedulers.ResolveEventMessage(context.Background(), event, dirs)
	if !available || text != "built\n1 warning\n" {
		t.Fatalf("message = %q available=%v", text, available)
	}
	// Without a step log, the sandbox cell is still read.
	event.LinkedCellID = "cell-2"
	if _, available := schedulers.ResolveEventMessage(context.Background(), event, dirs); available {
		t.Fatal("missing step log and sandbox reported as available")
	}
}

type staticSandboxDirs string

func (d staticSandboxDirs) SandboxDir(string) string { return string(d) }
