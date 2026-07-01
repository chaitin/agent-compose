package compose

import (
	"encoding/json"
	"testing"
)

func TestManifestSchemaJSONIsValid(t *testing.T) {
	var decoded map[string]any
	if err := json.Unmarshal(ManifestSchemaJSON(), &decoded); err != nil {
		t.Fatalf("ManifestSchemaJSON is not valid JSON: %v", err)
	}
	if decoded["$schema"] == "" || decoded["$defs"] == nil {
		t.Fatalf("schema missing expected top-level fields: %#v", decoded)
	}
}
