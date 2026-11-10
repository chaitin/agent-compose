package adapters

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func TestEgressAuditLogWritesDecisionLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "egress.jsonl")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	audit, err := NewEgressAuditLog(path, logger)
	if err != nil {
		t.Fatalf("NewEgressAuditLog() error = %v", err)
	}
	request := egress.NetworkRequest("sandbox-1", egress.Endpoint{Host: "allowed.example", Port: 443, Protocol: egress.ProtocolHTTPS})
	request.SessionID = "session-1"
	request.Marker = "run-1"
	request.ProcessID = 42
	request.Binary = "/usr/bin/python3"
	request.ResolvedIPs = []string{"93.184.216.34"}
	allowed := egress.Result{Action: egress.Allow, Target: "93.184.216.34", Generation: 7}
	denied := egress.Result{Action: egress.Deny, RuleID: "network.deny", Reason: "not declared", Generation: 7}
	audit.RecordEgressDecision(context.Background(), egress.NewRecord(request, allowed))
	audit.RecordEgressDecision(context.Background(), egress.NewRecord(request, denied))
	if err := audit.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	lines := 0
	for scanner.Scan() {
		lines++
		var record egress.AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode line %d: %v", lines, err)
		}
		if record.SandboxID != "sandbox-1" || record.SessionID != "session-1" || record.Marker != "run-1" || record.ProcessID != 42 {
			t.Fatalf("record %d identity = %+v", lines, record)
		}
		if record.Binary != "/usr/bin/python3" {
			t.Fatalf("record %d binary = %q, want the request executable", lines, record.Binary)
		}
		if record.Destination != "allowed.example:443/https" {
			t.Fatalf("record %d destination = %q", lines, record.Destination)
		}
		if record.Generation != 7 || record.Time.IsZero() {
			t.Fatalf("record %d = %+v, want a generation and a timestamp", lines, record)
		}
		if lines == 1 && record.Action != string(egress.Allow) {
			t.Fatalf("first record action = %q, want allow", record.Action)
		}
		if lines == 2 && (record.Action != string(egress.Deny) || record.RuleID != "network.deny") {
			t.Fatalf("second record = %+v, want the deny with its rule", record)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != 2 {
		t.Fatalf("lines = %d, want 2", lines)
	}
}
