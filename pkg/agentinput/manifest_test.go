package agentinput

import (
	"encoding/json"
	"testing"
)

func TestManifestMarshalsGuestsSchema(t *testing.T) {
	manifest := NewManifest([]ManifestPart{
		{Text: "compare"},
		{Media: &ManifestMedia{
			MediaType: MediaTypePNG,
			Filename:  "before.png",
			Path:      "/sandbox/state/agents/attachments/run-1/turn-0/part-1",
			SizeBytes: 42,
			SHA256:    sha256Hex(pngBytes()),
		}},
		{Media: &ManifestMedia{
			MediaType:     MediaTypePDF,
			Path:          "/sandbox/workspace/reports/q3.pdf",
			WorkspacePath: "reports/q3.pdf",
		}},
	})
	if manifest.Version != ManifestVersion {
		t.Fatalf("version = %d, want %d", manifest.Version, ManifestVersion)
	}
	data, err := manifest.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["version"] != float64(ManifestVersion) {
		t.Errorf("version = %v", decoded["version"])
	}
	parts, ok := decoded["parts"].([]any)
	if !ok || len(parts) != 3 {
		t.Fatalf("parts = %v", decoded["parts"])
	}
	first := parts[0].(map[string]any)
	if first["text"] != "compare" {
		t.Errorf("first part = %v", first)
	}
	second := parts[1].(map[string]any)["media"].(map[string]any)
	if second["mediaType"] != MediaTypePNG || second["path"] == "" {
		t.Errorf("second media = %v", second)
	}
	third := parts[2].(map[string]any)["media"].(map[string]any)
	if third["workspacePath"] != "reports/q3.pdf" {
		t.Errorf("third media = %v", third)
	}
}

func TestManifestOmitsOptionalFields(t *testing.T) {
	data, err := NewManifest([]ManifestPart{{Text: "hi"}}).Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"version":1,"parts":[{"text":"hi"}]}` {
		t.Errorf("manifest = %s", data)
	}
}
