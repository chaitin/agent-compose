package runs

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// abandonRunLog drops the run's open writer the way a daemon stopping does:
// nothing is flushed or committed, so reopening the log rolls it back to its
// last commit.
func abandonRunLog(t *testing.T, path string) {
	t.Helper()
	openRunLogs.mu.Lock()
	entry := openRunLogs.open[path]
	delete(openRunLogs.open, path)
	openRunLogs.mu.Unlock()
	if entry != nil && entry.writer != nil {
		if err := entry.writer.Abandon(); err != nil {
			t.Fatal(err)
		}
	}
}

// testJournal keeps the records a test delivered, so a simulated restart can
// deliver again those past the offset the projector resumes from, as reading
// the guest journal from that offset would.
type testJournal struct {
	chunks []domain.ExecChunk
}

func (j *testJournal) record(chunk domain.ExecChunk) domain.ExecChunk {
	j.chunks = append(j.chunks, chunk)
	return chunk
}

func (j *testJournal) after(offset uint64) []domain.ExecChunk {
	var out []domain.ExecChunk
	for _, chunk := range j.chunks {
		if chunk.Journal != nil && chunk.Journal.End > offset {
			out = append(out, chunk)
		}
	}
	return out
}
