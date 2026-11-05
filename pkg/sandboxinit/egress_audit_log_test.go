package sandboxinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEgressAuditLogWritesTheDaemonSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "egress-audit.jsonl")
	log, err := OpenEgressAuditLog(path, nil)
	if err != nil {
		t.Fatalf("OpenEgressAuditLog() error = %v", err)
	}
	at := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	log.now = func() time.Time { return at }
	log.Record(EgressAuditEvent{
		SandboxID: "sandbox-1", SessionID: "session-1", Marker: "marker-1",
		PID: 42, Family: "tcp", Host: "api.github.com", Port: 443, Generation: 9,
	})
	log.Record(EgressAuditEvent{
		SandboxID: "sandbox-1", SessionID: "session-1", Marker: "marker-1",
		PID: 43, Family: "unix", UnixPath: "/tmp/fixture.sock", Decision: "deny", Reason: "not declared",
	})
	if err := log.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := splitAuditLines(t, data)
	if len(lines) != 2 {
		t.Fatalf("audit lines = %d, want 2", len(lines))
	}
	var first egressAuditLine
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("decode first line: %v", err)
	}
	if !first.Time.Equal(at) || first.SandboxID != "sandbox-1" || first.SessionID != "session-1" ||
		first.Marker != "marker-1" || first.ProcessID != 42 || first.Generation != 9 {
		t.Fatalf("first line = %+v, want the sandbox/session/marker/pid/generation identity", first)
	}
	if first.Destination != "api.github.com:443" || first.Host != "api.github.com" || first.Port != 443 {
		t.Fatalf("first destination = %+v, want host and port", first)
	}
	if first.Action != "allow" || first.Decision != "audit" {
		t.Fatalf("observed action/decision = %q/%q, want allow/audit", first.Action, first.Decision)
	}

	var second egressAuditLine
	if err := json.Unmarshal(lines[1], &second); err != nil {
		t.Fatalf("decode second line: %v", err)
	}
	if second.Destination != "/tmp/fixture.sock" || second.UnixPath != "/tmp/fixture.sock" {
		t.Fatalf("second destination = %+v, want the unix path", second)
	}
	if second.Action != "deny" || second.Decision != "deny" || second.Reason != "not declared" {
		t.Fatalf("second decision = %+v, want the mediated deny", second)
	}
}

func TestEgressAuditLogAppendsWithoutOverwritingExistingLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "egress-audit.jsonl")
	if err := os.WriteFile(path, []byte("existing\n"), 0o600); err != nil {
		t.Fatalf("seed audit log: %v", err)
	}
	log, err := OpenEgressAuditLog(path, nil)
	if err != nil {
		t.Fatalf("OpenEgressAuditLog() error = %v", err)
	}
	log.Record(EgressAuditEvent{SandboxID: "sandbox-1", Host: "example.com", Port: 80})
	if err := log.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	lines := splitAuditLines(t, data)
	if len(lines) != 2 || string(lines[0]) != "existing" {
		t.Fatalf("lines = %q, want the existing line followed by one event", lines)
	}
}

func TestParseEgressAuditLogPathAndGeneration(t *testing.T) {
	if path, err := ParseEgressAuditLogPath("  "); err != nil || path != "" {
		t.Fatalf("ParseEgressAuditLogPath(empty) = %q, %v; want disabled", path, err)
	}
	if path, err := ParseEgressAuditLogPath(" /data/logs/egress-audit.jsonl "); err != nil || path != "/data/logs/egress-audit.jsonl" {
		t.Fatalf("ParseEgressAuditLogPath() = %q, %v", path, err)
	}
	if _, err := ParseEgressAuditLogPath("relative.jsonl"); err == nil {
		t.Fatal("ParseEgressAuditLogPath() accepted a relative path")
	}
	if generation, err := ParseEgressGeneration(" 12 "); err != nil || generation != 12 {
		t.Fatalf("ParseEgressGeneration() = %d, %v", generation, err)
	}
	if _, err := ParseEgressGeneration("not-a-number"); err == nil {
		t.Fatal("ParseEgressGeneration() accepted a non-numeric value")
	}
}

func splitAuditLines(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var lines [][]byte
	start := 0
	for index, value := range data {
		if value != '\n' {
			continue
		}
		lines = append(lines, data[start:index])
		start = index + 1
	}
	if start != len(data) {
		t.Fatalf("audit log does not end with a newline: %q", data)
	}
	return lines
}
