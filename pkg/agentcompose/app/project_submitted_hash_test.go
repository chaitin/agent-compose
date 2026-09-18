package app

import (
	"testing"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestNormalizeProjectRequestSubmittedSpecHashContract(t *testing.T) {
	spec := &agentcomposev2.ProjectSpec{Name: "hash-contract"}
	normalized, issues, err := normalizeProjectRequest(t.Context(), spec, nil, "")
	if err != nil || len(issues) != 0 {
		t.Fatalf("normalize without submitted hash: issues=%#v err=%v", issues, err)
	}

	for _, tc := range []struct {
		name          string
		submittedHash string
		wantIssue     bool
	}{
		{name: "empty skips validation"},
		{name: "matching normalized hash", submittedHash: normalized.SpecHash},
		{name: "mismatch reports submitted field", submittedHash: "sha256:not-the-submitted-spec", wantIssue: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, gotIssues, gotErr := normalizeProjectRequest(t.Context(), spec, nil, tc.submittedHash)
			if gotErr != nil {
				t.Fatalf("normalize: %v", gotErr)
			}
			if got.SpecHash != normalized.SpecHash {
				t.Fatalf("spec hash = %q, want %q", got.SpecHash, normalized.SpecHash)
			}
			if tc.wantIssue {
				if len(gotIssues) != 1 || gotIssues[0].Path != "submitted_spec_hash" {
					t.Fatalf("issues = %#v, want submitted_spec_hash mismatch", gotIssues)
				}
				return
			}
			if len(gotIssues) != 0 {
				t.Fatalf("issues = %#v, want none", gotIssues)
			}
		})
	}
}
