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
