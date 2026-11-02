package sandboxinit

import (
	"encoding/json"
	"testing"
)

func TestFrameMarshalIncludesZeroExitCode(t *testing.T) {
	payload, err := json.Marshal(Frame{Type: FrameExit, SessionID: "s", Code: 0})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if code, ok := decoded["code"]; !ok || code != float64(0) {
		t.Fatalf("exit frame = %s, want an explicit code:0", payload)
	}

	payload, err = json.Marshal(Frame{Type: FramePong})
	if err != nil {
		t.Fatal(err)
	}
	decoded = nil
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["code"]; ok {
		t.Fatalf("pong frame = %s, want no code field", payload)
	}
}

func TestFramePongRoundTripsHandshake(t *testing.T) {
	payload, err := json.Marshal(Frame{
		Type:         FramePong,
		SandboxID:    "sandbox-1",
		Capabilities: []string{CapabilityExec, CapabilitySandboxIdentity},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded Frame
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SandboxID != "sandbox-1" {
		t.Fatalf("SandboxID = %q, want %q", decoded.SandboxID, "sandbox-1")
	}
	if len(decoded.Capabilities) != 2 || decoded.Capabilities[0] != CapabilityExec || decoded.Capabilities[1] != CapabilitySandboxIdentity {
		t.Fatalf("Capabilities = %v, want the advertised handshake", decoded.Capabilities)
	}
}
