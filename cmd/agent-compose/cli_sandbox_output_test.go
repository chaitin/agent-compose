package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComposeSandboxPruneOutputJSONShape(t *testing.T) {
	output := composeSandboxPruneOutput{
		DryRun: true,
		Matched: []composePSSandboxOutput{{
			SandboxID:      "sandbox-match",
			SandboxShortID: "sandbox-match",
			Agent:          "worker",
			Status:         "stopped",
			UpdatedAt:      "2026-06-11T00:00:00Z",
			Driver:         "boxlite",
		}},
		Removed: []string{"sandbox-removed"},
		Skipped: []composeSandboxPruneSkipped{{
			SandboxID: "sandbox-skipped",
			Agent:     "worker",
			Status:    "failed",
			UpdatedAt: "2026-06-11T00:00:00Z",
			Driver:    "boxlite",
			Reason:    "remove failed: denied",
		}},
		Warnings: []string{"scan warning"},
	}
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal composeSandboxPruneOutput: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode composeSandboxPruneOutput JSON: %v\n%s", err, data)
	}
	for _, key := range []string{"dry_run", "matched", "removed", "skipped", "warnings"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("composeSandboxPruneOutput JSON %s missing key %q", data, key)
		}
	}
	if strings.Contains(string(data), "DryRun") || strings.Contains(string(data), "Sandbox") || strings.Contains(string(data), "Reason") {
		t.Fatalf("composeSandboxPruneOutput JSON uses Go field names: %s", data)
	}
	var matched []map[string]json.RawMessage
	if err := json.Unmarshal(decoded["matched"], &matched); err != nil {
		t.Fatalf("decode matched sandboxes: %v", err)
	}
	if _, ok := matched[0]["sandbox_id"]; !ok {
		t.Fatalf("matched sandbox JSON missing sandbox_id: %s", data)
	}
	if _, ok := matched[0]["id"]; ok {
		t.Fatalf("matched sandbox JSON uses id: %s", data)
	}
	var skipped []map[string]json.RawMessage
	if err := json.Unmarshal(decoded["skipped"], &skipped); err != nil {
		t.Fatalf("decode skipped sandboxes: %v", err)
	}
	if _, ok := skipped[0]["sandbox_id"]; !ok {
		t.Fatalf("skipped sandbox JSON missing sandbox_id: %s", data)
	}
	if _, ok := skipped[0]["sandbox"]; ok {
		t.Fatalf("skipped sandbox JSON uses sandbox: %s", data)
	}
	if !strings.Contains(string(data), `"updated_at"`) {
		t.Fatalf("composeSandboxPruneOutput JSON missing skipped metadata: %s", data)
	}
}
