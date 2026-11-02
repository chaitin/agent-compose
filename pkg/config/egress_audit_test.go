package config

import "testing"

func TestEgressAuditLogDefaultsToDisabled(t *testing.T) {
	t.Setenv(EgressAuditLogName, "")
	path, err := loadEgressAuditLogPath()
	if err != nil {
		t.Fatalf("loadEgressAuditLogPath() error = %v", err)
	}
	if path != "" {
		t.Fatalf("path = %q, want empty (disabled)", path)
	}
}

func TestEgressAuditLogRequiresAnAbsolutePath(t *testing.T) {
	t.Setenv(EgressAuditLogName, "relative/egress.jsonl")
	if _, err := loadEgressAuditLogPath(); err == nil {
		t.Fatal("loadEgressAuditLogPath accepted a relative path")
	}
	t.Setenv(EgressAuditLogName, "/data/audit/egress.jsonl")
	path, err := loadEgressAuditLogPath()
	if err != nil {
		t.Fatalf("loadEgressAuditLogPath() error = %v", err)
	}
	if path != "/data/audit/egress.jsonl" {
		t.Fatalf("path = %q, want the configured file", path)
	}
}
