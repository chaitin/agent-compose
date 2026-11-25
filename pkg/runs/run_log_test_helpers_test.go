package runs

import (
	"os"
	"path/filepath"

	"github.com/chaitin/agent-compose/pkg/runlog"
)

// readRunLogText returns a run's text view, whether path is a run log or a
// legacy text transcript.
func readRunLogText(path string) (string, error) {
	if filepath.Base(path) != runlog.FileName {
		data, err := os.ReadFile(path)
		return string(data), err
	}
	entries, err := runlog.ReadEntries(path, 0)
	if err != nil {
		return "", err
	}
	return runlog.RenderText(entries), nil
}
