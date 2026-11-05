package sandboxinit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EgressAuditLog is the init-side append-only JSONL sink for observed
// connections. Its schema mirrors the daemon's EGRESS_AUDIT_LOG record: one
// line per event with the sandbox and session attribution, destination, action,
// reason, and policy generation. Init never makes a policy decision, so an
// observation carries action "allow" (the connection was not changed) and
// decision "audit" unless a mediated supervisor supplied a real decision.
type EgressAuditLog struct {
	mu     sync.Mutex
	file   *os.File
	logger *slog.Logger
	now    func() time.Time
}

// egressAuditLine is the persisted shape. Field names match the daemon's
// egress.AuditRecord where the concepts overlap, and add host/port/decision so a
// line is useful without parsing the destination string.
type egressAuditLine struct {
	Time        time.Time `json:"time"`
	SandboxID   string    `json:"sandbox_id,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Destination string    `json:"destination,omitempty"`
	Host        string    `json:"host,omitempty"`
	Port        int       `json:"port,omitempty"`
	UnixPath    string    `json:"unix_path,omitempty"`
	Action      string    `json:"action,omitempty"`
	Decision    string    `json:"decision,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Generation  uint64    `json:"generation,omitempty"`
	SessionID   string    `json:"session_id,omitempty"`
	Marker      string    `json:"marker,omitempty"`
	ProcessID   int       `json:"process_id,omitempty"`
	Binary      string    `json:"binary,omitempty"`
}

// ParseEgressAuditLogPath validates the guest path the daemon injected. Empty
// disables the sink; a relative path is rejected because init runs in a guest
// whose working directory is not the daemon's.
func ParseEgressAuditLogPath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be an absolute guest path, got %q", EgressAuditLogEnvVar, raw)
	}
	return filepath.Clean(path), nil
}

// ParseEgressGeneration parses the advisory launch generation. Empty is zero,
// which the sink omits from the line.
func ParseEgressGeneration(raw string) (uint64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, nil
	}
	generation, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer, got %q", EgressGenerationEnvVar, raw)
	}
	return generation, nil
}

// OpenEgressAuditLog opens or creates the audit file for append. The parent
// directory is created so the injected path can be the first writer under a
// mounted log root. The file is 0600 because destinations and markers are
// sensitive.
func OpenEgressAuditLog(path string, logger *slog.Logger) (*EgressAuditLog, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if directory := filepath.Dir(path); directory != "" && directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, fmt.Errorf("create sandbox init audit log directory %s: %w", directory, err)
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open sandbox init audit log %s: %w", path, err)
	}
	return &EgressAuditLog{file: file, logger: logger, now: time.Now}, nil
}

// Record appends one event. O_APPEND and one write per line keep the file safe
// under concurrent observers and under an external rotation: a writer never
// overwrites an existing line, and a line is never split across writes by this
// process. A write failure is reported but cannot change the connection, because
// audit is not the decision path.
func (l *EgressAuditLog) Record(event EgressAuditEvent) {
	if l == nil || l.file == nil {
		return
	}
	line := egressAuditLineFrom(event, l.now().UTC())
	data, err := json.Marshal(line)
	if err != nil {
		l.logger.Error("encode sandbox init audit record", "sandbox_id", event.SandboxID, "error", err)
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.file.Write(append(data, '\n')); err != nil {
		l.logger.Error("write sandbox init audit record", "sandbox_id", event.SandboxID, "error", err)
	}
}

// Close releases the file. The caller owns the sink lifecycle.
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

func egressAuditLineFrom(event EgressAuditEvent, at time.Time) egressAuditLine {
	line := egressAuditLine{
		Time:       at,
		SandboxID:  event.SandboxID,
		Kind:       "connect",
		Host:       event.Host,
		Port:       event.Port,
		UnixPath:   event.UnixPath,
		Action:     event.Decision,
		Decision:   event.Decision,
		Reason:     event.Reason,
		Generation: event.Generation,
		SessionID:  event.SessionID,
		Marker:     event.Marker,
		ProcessID:  event.PID,
		Binary:     event.Binary,
	}
	if line.Action == "" {
		// Observation mode did not change the syscall, so the connection was
		// allowed; the empty decision is recorded as "audit" to keep it
		// distinguishable from a policy allow.
		line.Action = "allow"
		line.Decision = "audit"
	}
	switch {
	case event.UnixPath != "":
		line.Destination = event.UnixPath
	case event.Host != "":
		line.Destination = net.JoinHostPort(event.Host, strconv.Itoa(event.Port))
	}
	return line
}
