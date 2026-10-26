package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewDaemonAppReportsInvalidGlobalPolicyAsError pins the startup behavior:
// an invalid EGRESS_GLOBAL_POLICY_FILE must surface as a returned error, not as
// the panic MustInvoke raises for a provider that failed. The daemon still fails
// closed; only the presentation changes.
func TestNewDaemonAppReportsInvalidGlobalPolicyAsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "global-policy.yaml")
	policy := "network:\n  default: deny-all\n  allow:\n    - host: api.example.com\n      port: 443\n      protocol: any\n"
	if err := os.WriteFile(path, []byte(policy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	t.Setenv("EGRESS_GLOBAL_POLICY_FILE", path)

	_, err := NewDaemonApp(context.Background(), DaemonOptions{})
	if err == nil {
		t.Fatal("NewDaemonApp accepted an invalid EGRESS_GLOBAL_POLICY_FILE")
	}
	if !strings.Contains(err.Error(), "EGRESS_GLOBAL_POLICY_FILE") {
		t.Fatalf("NewDaemonApp error = %v, want the policy file name", err)
	}
	if strings.Contains(err.Error(), "goroutine") || strings.Contains(err.Error(), "panic") {
		t.Fatalf("NewDaemonApp leaked a panic instead of a startup error: %v", err)
	}
}
