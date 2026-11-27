package runs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

func TestProjectRunLogIsCappedButKeepsItsOutcome(t *testing.T) {
	if runLogMaxBytes != runlog.DefaultRunLogMaxBytes {
		t.Fatalf("run logs are capped at %d, want %d", runLogMaxBytes, runlog.DefaultRunLogMaxBytes)
	}
	saved := runLogMaxBytes
	runLogMaxBytes = 4 << 10
	t.Cleanup(func() { runLogMaxBytes = saved })

	path := filepath.Join(t.TempDir(), "runs", "r", runlog.FileName)
	run := domain.ProjectRunRecord{RunID: "r", LogFormat: runlog.FormatJSONL, LogsPath: path}
	w, err := runLogWriter(context.Background(), run, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := w.AppendOutput("stdout", strings.Repeat("x", 1<<10)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.EndTurn("stop", "done", nil); err != nil {
		t.Fatal(err)
	}
	if err := closeRunLog(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	entries, err := runlog.ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kept, dropped uint64
	for _, e := range entries {
		switch e.Type {
		case runlog.EntryOutput:
			kept += uint64(len(e.Data))
		case runlog.EntryTruncated:
			dropped += e.DroppedBytes
		}
	}
	// The final text is recorded on the turn end only; the cap drops the
	// output past its limit and keeps the turn end.
	last := entries[len(entries)-1]
	if kept+dropped != 20<<10 || dropped == 0 || last.Type != runlog.EntryTurnEnd || last.FinalText != "done" {
		t.Fatalf("kept %d, dropped %d, last %+v", kept, dropped, last)
	}
}
