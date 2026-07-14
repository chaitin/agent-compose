package runs

import "testing"

func TestRunEventIDsAreStableAndTypeSeparated(t *testing.T) {
	runID := "run-event-identity"
	frame := []byte("assistant frame")
	identities := []string{
		initialPromptEventID(runID),
		attachedHumanEventID(runID, "frame-1", 1, "question"),
		attachedAgentEventID(runID, 1, frame),
		terminalAgentEventID(runID),
		terminalStatusEventID(runID),
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity == "" {
			t.Fatal("empty run event id")
		}
		if _, exists := seen[identity]; exists {
			t.Fatalf("duplicate run event id %q", identity)
		}
		seen[identity] = struct{}{}
	}
	if got := attachedHumanEventID(runID, " frame-1 ", 99, "changed"); got != identities[1] {
		t.Fatalf("client frame retry id = %q, want %q", got, identities[1])
	}
	if got := attachedAgentEventID(runID, 1, []byte("changed")); got != identities[2] {
		t.Fatalf("sequenced agent retry id = %q, want %q", got, identities[2])
	}
}

func TestRunEventFallbackIDsIncludeFrameIdentity(t *testing.T) {
	runID := "run-event-fallback"
	human := attachedHumanEventID(runID, "", 1, "question")
	if repeated := attachedHumanEventID(runID, "", 1, "question"); repeated != human {
		t.Fatalf("human fallback retry id = %q, want %q", repeated, human)
	}
	if changedIndex := attachedHumanEventID(runID, "", 2, "question"); changedIndex == human {
		t.Fatal("different human frame index produced the same event id")
	}
	agent := attachedAgentEventID(runID, 0, []byte("frame-one"))
	if repeated := attachedAgentEventID(runID, 0, []byte("frame-one")); repeated != agent {
		t.Fatalf("agent fallback retry id = %q, want %q", repeated, agent)
	}
	if changedFrame := attachedAgentEventID(runID, 0, []byte("frame-two")); changedFrame == agent {
		t.Fatal("different agent frame produced the same event id")
	}
}
