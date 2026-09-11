package api

import (
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestPaginateSandboxHistoryUsesOneStableTimeline(t *testing.T) {
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cells := []domain.NotebookCell{
		{ID: "cell-newest", CreatedAt: base.Add(3 * time.Minute)},
		{ID: "cell-oldest", CreatedAt: base},
	}
	events := []domain.SandboxEvent{
		{ID: "event-middle", CreatedAt: base.Add(2 * time.Minute)},
		{ID: "event-older", CreatedAt: base.Add(time.Minute)},
	}

	response, err := paginateSandboxHistory(cells, events, 1, 2)
	if err != nil {
		t.Fatalf("paginateSandboxHistory returned error: %v", err)
	}
	if response.GetTotal() != 4 {
		t.Fatalf("total = %d, want 4", response.GetTotal())
	}
	if len(response.GetCells()) != 0 {
		t.Fatalf("cells = %#v, want no cells on the middle page", response.GetCells())
	}
	if got := response.GetEvents(); len(got) != 2 || got[0].GetId() != "event-middle" || got[1].GetId() != "event-older" {
		t.Fatalf("events = %#v, want middle timeline entries", got)
	}
}

func TestPaginateSandboxHistoryBreaksTimestampTiesDeterministically(t *testing.T) {
	createdAt := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	cells := []domain.NotebookCell{{ID: "cell-b", CreatedAt: createdAt}, {ID: "cell-a", CreatedAt: createdAt}}
	events := []domain.SandboxEvent{{ID: "event-z", CreatedAt: createdAt}}

	first, err := paginateSandboxHistory(cells, events, 0, 1)
	if err != nil {
		t.Fatalf("first page returned error: %v", err)
	}
	second, err := paginateSandboxHistory(cells, events, 1, 1)
	if err != nil {
		t.Fatalf("second page returned error: %v", err)
	}
	third, err := paginateSandboxHistory(cells, events, 2, 1)
	if err != nil {
		t.Fatalf("third page returned error: %v", err)
	}
	if first.GetCells()[0].GetId() != "cell-b" || second.GetCells()[0].GetId() != "cell-a" || third.GetEvents()[0].GetId() != "event-z" {
		t.Fatalf("tie order = (%v, %v, %v), want cell-b, cell-a, event-z", first, second, third)
	}
}

func TestPaginateSandboxHistoryCapsCellOutput(t *testing.T) {
	buildLog := strings.Repeat("a", maxSandboxHistoryCellOutputBytes+4096)
	cells := []domain.NotebookCell{{ID: "cell-build", Output: buildLog, Running: true}}

	response, err := paginateSandboxHistory(cells, nil, 0, 0)
	if err != nil {
		t.Fatalf("paginateSandboxHistory returned error: %v", err)
	}
	cell := response.GetCells()[0]
	if got := len(cell.GetOutput()); got != maxSandboxHistoryCellOutputBytes {
		t.Fatalf("output length = %d, want %d", got, maxSandboxHistoryCellOutputBytes)
	}
	if got := cell.GetOutputTruncatedBytes(); got != 4096 {
		t.Fatalf("output_truncated_bytes = %d, want 4096", got)
	}
	if !strings.HasSuffix(buildLog, cell.GetOutput()) {
		t.Fatal("output is not the tail of the captured stream")
	}
}

func TestPaginateSandboxHistorySendsOneCopyOfTheCapturedStream(t *testing.T) {
	cells := []domain.NotebookCell{
		{ID: "cell-merged", Stdout: "out", Stderr: "err", Output: "outerr"},
		{ID: "cell-legacy", Stdout: "out", Stderr: "err"},
	}

	response, err := paginateSandboxHistory(cells, nil, 0, 0)
	if err != nil {
		t.Fatalf("paginateSandboxHistory returned error: %v", err)
	}
	for _, cell := range response.GetCells() {
		if cell.GetStdout() != "" || cell.GetStderr() != "" {
			t.Fatalf("%s still carries a second copy: stdout = %q, stderr = %q", cell.GetId(), cell.GetStdout(), cell.GetStderr())
		}
		if cell.GetOutput() != "outerr" {
			t.Fatalf("%s output = %q, want the merged stream", cell.GetId(), cell.GetOutput())
		}
	}
}

func TestPaginateSandboxHistoryKeepsTruncatedOutputMarshalable(t *testing.T) {
	// A cut through a multi-byte rune leaves a proto3 string that no longer
	// marshals, which would fail the whole response rather than one cell.
	cells := []domain.NotebookCell{{ID: "cell-cjk", Output: strings.Repeat("日", maxSandboxHistoryCellOutputBytes)}}

	response, err := paginateSandboxHistory(cells, nil, 0, 0)
	if err != nil {
		t.Fatalf("paginateSandboxHistory returned error: %v", err)
	}
	output := response.GetCells()[0].GetOutput()
	if !utf8.ValidString(output) {
		t.Fatal("truncated output is not valid UTF-8")
	}
	if len(output) > maxSandboxHistoryCellOutputBytes {
		t.Fatalf("output length = %d, want at most %d", len(output), maxSandboxHistoryCellOutputBytes)
	}
	if _, err := proto.Marshal(response); err != nil {
		t.Fatalf("marshal truncated response: %v", err)
	}
}

func TestPaginateSandboxHistoryCapsALegacyCellsMergedStreams(t *testing.T) {
	// A cell recorded before the streams were merged has no Output, so the
	// concatenation is what gets sent. Nothing exercised that branch against a
	// stream large enough to truncate, which is the only size that matters here.
	const halfStream = 40 << 10
	stdout, stderr := strings.Repeat("a", halfStream), strings.Repeat("e", halfStream)
	cells := []domain.NotebookCell{{ID: "cell-legacy", Stdout: stdout, Stderr: stderr}}

	response, err := paginateSandboxHistory(cells, nil, 0, 0)
	if err != nil {
		t.Fatalf("paginateSandboxHistory returned error: %v", err)
	}
	cell := response.GetCells()[0]
	if got := len(cell.GetOutput()); got != maxSandboxHistoryCellOutputBytes {
		t.Fatalf("output length = %d, want %d", got, maxSandboxHistoryCellOutputBytes)
	}
	if want := uint64(2*halfStream - maxSandboxHistoryCellOutputBytes); cell.GetOutputTruncatedBytes() != want {
		t.Fatalf("output_truncated_bytes = %d, want %d", cell.GetOutputTruncatedBytes(), want)
	}
	// The tail spans the seam, so it proves the two streams were joined in order
	// rather than one of them being sent alone.
	if !strings.HasSuffix(stdout+stderr, cell.GetOutput()) {
		t.Fatal("output is not the tail of the concatenated streams")
	}
	if !strings.Contains(cell.GetOutput(), "ae") {
		t.Fatal("output does not span the stdout/stderr seam")
	}
}

func TestSandboxHistoryCellDoesNotCopyAStreamTheMergeDiscards(t *testing.T) {
	// Go evaluates arguments before the call, so spelling the merge as
	// firstNonEmpty(cell.Output, cell.Stdout+cell.Stderr) builds the concatenation
	// for every cell and discards it whenever Output is set - which is every cell
	// written since the streams were merged. Both halves have to be non-empty for
	// this to bite: Go returns s unchanged for s + "".
	const streamBytes = 4 << 20
	cell := domain.NotebookCell{
		ID:     "cell-build",
		Stdout: strings.Repeat("a", streamBytes),
		Stderr: strings.Repeat("e", streamBytes),
		Output: strings.Repeat("o", 2*streamBytes),
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	converted := sandboxHistoryCellToV2(&cell)
	runtime.ReadMemStats(&after)

	if got := len(converted.GetOutput()); got != maxSandboxHistoryCellOutputBytes {
		t.Fatalf("output length = %d, want %d", got, maxSandboxHistoryCellOutputBytes)
	}
	// Converting a cell allocates the response message and nothing stream-sized:
	// a couple of hundred bytes in practice, against 8MB if the discarded
	// concatenation comes back. Any threshold between the two would do.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > streamBytes {
		t.Fatalf("converting one cell allocated %d bytes, want far less than one stream (%d)", allocated, streamBytes)
	}
}

func TestPaginateSandboxHistoryKeepsOutputMarshalableWhenTheRunWroteBinary(t *testing.T) {
	// A cell holds the bytes its run wrote, and a command that cats a binary
	// writes bytes that are not text. A proto3 string field rejects those on
	// marshal, failing the whole response rather than the one cell - the same
	// blast radius the byte cap exists to contain. Truncation alone does not help:
	// a stream under the cap is passed through untouched.
	binary := string([]byte{0xff, 0xfe, 0x00, 0x81}) + "tail-of-log"
	cells := []domain.NotebookCell{
		{ID: "cell-small-binary", Output: binary},
		{ID: "cell-large-binary", Output: strings.Repeat(binary, maxSandboxHistoryCellOutputBytes)},
		{ID: "cell-legacy-binary", Stdout: binary, Stderr: binary},
	}

	response, err := paginateSandboxHistory(cells, nil, 0, 0)
	if err != nil {
		t.Fatalf("paginateSandboxHistory returned error: %v", err)
	}
	for _, cell := range response.GetCells() {
		if !utf8.ValidString(cell.GetOutput()) {
			t.Fatalf("%s output is not valid UTF-8", cell.GetId())
		}
		if !strings.HasSuffix(cell.GetOutput(), "tail-of-log") {
			t.Fatalf("%s output = %q, want the readable tail preserved", cell.GetId(), cell.GetOutput())
		}
	}
	if _, err := proto.Marshal(response); err != nil {
		t.Fatalf("marshal response carrying binary output: %v", err)
	}
}
