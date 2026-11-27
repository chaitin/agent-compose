package runs

import (
	"context"
	"path/filepath"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestIntegrationRunOutputPreservesEventStreamAttribution(t *testing.T) {
	for _, format := range []string{runlog.FormatJSONL, "text"} {
		t.Run(format, func(t *testing.T) {
			name := runlog.FileName
			if format == "text" {
				name = "transcript.txt"
			}
			run := domain.ProjectRunRecord{
				RunID: "run-1", ProjectID: "project-1", SandboxID: "sandbox-1",
				LogFormat: format, LogsPath: filepath.Join(t.TempDir(), name),
			}
			t.Cleanup(func() {
				if err := closeRunLog(context.Background(), run.LogsPath); err != nil {
					t.Error(err)
				}
			})
			hub := NewRunLogHub()
			var events []RunLogEvent
			hub.SetObserver(func(event RunLogEvent) { events = append(events, event) })
			chunks := []domain.ExecChunk{
				{Stream: domain.StdioStdout, Text: "output\n"},
				{Stream: domain.StdioStderr, Text: "diagnostic\n"},
			}
			for _, chunk := range chunks {
				if err := appendRunOutput(t.Context(), hub, run, run.LogsPath, chunk); err != nil {
					t.Fatal(err)
				}
			}
			if err := closeRunLog(t.Context(), run.LogsPath); err != nil {
				t.Fatal(err)
			}
			if len(events) != len(chunks) {
				t.Fatalf("observed events = %+v, want one per output chunk", events)
			}
			var offset uint64
			for i, event := range events {
				chunk := chunks[i]
				offset += uint64(len(chunk.Text))
				if event.RunID != run.RunID || event.ProjectID != run.ProjectID || event.SandboxID != run.SandboxID {
					t.Fatalf("output lost event attribution: %+v", event)
				}
				if event.Stream != string(chunk.Stream) || event.Data != chunk.Text || event.Offset != offset || event.CreatedAt.IsZero() {
					t.Fatalf("output event = %+v, want chunk %+v at offset %d", event, chunk, offset)
				}
				if format == runlog.FormatJSONL && event.Seq != uint64(i+1) {
					t.Fatalf("output sequence = %d, want %d", event.Seq, i+1)
				}
			}
			text, err := readRunLogText(run.LogsPath)
			if err != nil || text != "output\ndiagnostic\n" {
				t.Fatalf("persisted output = %q, error = %v", text, err)
			}
		})
	}
}
