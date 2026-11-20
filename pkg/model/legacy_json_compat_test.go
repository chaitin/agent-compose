package model

import (
	"encoding/json"
	"testing"
)

func TestLegacyAgentSessionJSONCompatibility(t *testing.T) {
	var cell NotebookCell
	if err := json.Unmarshal([]byte(`{
		"id":"cell-1",
		"agent_session_id":"thread-1",
		"agent_resume":{
			"session_id":"thread-1",
			"session_state_path":"/state.json",
			"session_manifest_path":"/manifest.json",
			"session_jsonl_paths":["/log.jsonl"]
		}
	}`), &cell); err != nil {
		t.Fatal(err)
	}
	if cell.AgentThreadID != "thread-1" {
		t.Fatalf("legacy cell decoded as %#v", cell)
	}

	// Cells that recorded the thread only inside agent_resume still surface it.
	for _, raw := range []string{
		`{"id":"cell-2","agent_resume":{"thread_id":"thread-3","provider_log_paths":["/log.jsonl"]}}`,
		`{"id":"cell-3","agent_resume":{"session_id":"thread-3","session_jsonl_paths":["/log.jsonl"]}}`,
	} {
		var resumeOnly NotebookCell
		if err := json.Unmarshal([]byte(raw), &resumeOnly); err != nil {
			t.Fatal(err)
		}
		if resumeOnly.AgentThreadID != "thread-3" {
			t.Fatalf("resume-only cell %s decoded as %#v", raw, resumeOnly)
		}
	}
	var explicit NotebookCell
	if err := json.Unmarshal([]byte(`{"id":"cell-4","agent_thread_id":"thread-4","agent_resume":{"thread_id":"stale"}}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.AgentThreadID != "thread-4" {
		t.Fatalf("explicit thread id overridden: %#v", explicit)
	}

	var run AgentRun
	if err := json.Unmarshal([]byte(`{"id":"run-1","agent_session_id":"thread-2"}`), &run); err != nil {
		t.Fatal(err)
	}
	if run.AgentThreadID != "thread-2" {
		t.Fatalf("legacy run decoded as %#v", run)
	}
}
