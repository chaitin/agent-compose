package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// EgressAuditLog is the append-only JSONL recorder for forward-proxy
// decisions. One line is one decision; the file is the audit surface the
// mediation path previously only logged to stderr.
type EgressAuditLog struct {
	mu     sync.Mutex
	file   *os.File
	logger *slog.Logger
	now    func() time.Time
}

// NewEgressAuditLog opens (or creates) the audit log. The parent directory is
// created so a deployment can point the setting at an empty DATA_ROOT
// subdirectory; the file itself is 0600 because destination names and session
// markers are sensitive.
func NewEgressAuditLog(path string, logger *slog.Logger) (*EgressAuditLog, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if directory := filepath.Dir(path); directory != "" && directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create egress audit log directory %s: %w", directory, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open egress audit log %s: %w", path, err)
	}
	return &EgressAuditLog{file: file, logger: logger, now: time.Now}, nil
}

// RecordEgressDecision appends one flattened decision. The recorder interface
// cannot return an error, so a write failure is logged and the decision itself
// is still enforced: persistence is audit, not the decision path.
func (l *EgressAuditLog) RecordEgressDecision(ctx context.Context, record egress.Record) {
	if l == nil || l.file == nil {
		return
	}
	entry := egress.AuditRecordFrom(record, l.now())
	line, err := json.Marshal(entry)
	if err != nil {
		l.logger.ErrorContext(ctx, "encode egress audit record", "sandbox_id", entry.SandboxID, "error", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		l.logger.ErrorContext(ctx, "write egress audit record", "sandbox_id", entry.SandboxID, "error", err)
	}
}

// Close releases the audit file. The daemon calls it at shutdown; tests call it
// before reading the file.
func (l *EgressAuditLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	file := l.file
	l.file = nil
	return file.Close()
}
