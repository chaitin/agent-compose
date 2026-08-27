package compose

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentJSONSchemasAreOptionalAndIndependent(t *testing.T) {
	spec, err := Parse([]byte(`
name: schemas
agents:
  input-only:
    input_schema:
      type: object
      description: Request accepted by the agent
      properties:
        query:
          type: string
          description: Search query
  output-only:
    output_schema: false
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Agents[0].InputSchema == nil || normalized.Agents[0].OutputSchema != nil {
		t.Fatalf("input-only schemas = %#v/%#v", normalized.Agents[0].InputSchema, normalized.Agents[0].OutputSchema)
	}
	if normalized.Agents[1].InputSchema != nil || normalized.Agents[1].OutputSchema == nil {
		t.Fatalf("output-only schemas = %#v/%#v", normalized.Agents[1].InputSchema, normalized.Agents[1].OutputSchema)
	}
	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	roundTrip, err := ParseCanonicalJSON(data)
	if err != nil {
		t.Fatalf("ParseCanonicalJSON returned error: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(*roundTrip.Agents[0].InputSchema, &schema); err != nil {
		t.Fatalf("decode input schema: %v", err)
	}
	if schema["description"] != "Request accepted by the agent" {
		t.Fatalf("input schema = %#v", schema)
	}
}

func TestAgentJSONSchemaFileSourceIsSnapshotted(t *testing.T) {
	dir := t.TempDir()
	schemaPath := filepath.Join(dir, "request.schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"type":"object","properties":{"count":{"type":"integer"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      provider: file\n      path: request.schema.json\n"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(dir, "agent-compose.yml"), ResolveSchemaURLs: true})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if normalized.Agents[0].InputSchema == nil || !strings.Contains(string(*normalized.Agents[0].InputSchema), `"count"`) {
		t.Fatalf("resolved input schema = %v", normalized.Agents[0].InputSchema)
	}
}

func TestUnresolvedAgentJSONSchemaSourceCannotBePersisted(t *testing.T) {
	spec, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      provider: file\n      path: request.schema.json\n"))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if _, err := normalized.Redacted().MarshalCanonicalJSON(false); err == nil || !strings.Contains(err.Error(), "input_schema") {
		t.Fatalf("MarshalCanonicalJSON error = %v", err)
	}
}

func TestAgentJSONSchemaRejectsNonSchemaDocument(t *testing.T) {
	_, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema: [string]\n"))
	if err == nil || !strings.Contains(err.Error(), "JSON Schema") {
		t.Fatalf("Parse error = %v", err)
	}
}

func TestAgentJSONSchemaPreservesLargeNumbers(t *testing.T) {
	spec, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      type: integer\n      maximum: 9223372036854775807\n"))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(*normalized.Agents[0].InputSchema); !strings.Contains(got, "9223372036854775807") {
		t.Fatalf("schema = %s", got)
	}
}

func TestAgentJSONSchemaProviderKeywordRoundTripsAsInlineSchema(t *testing.T) {
	spec, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      type: object\n      provider: custom-keyword\n"))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseCanonicalJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Agents[0].InputSchema == nil {
		t.Fatal("input schema was lost")
	}
}

func TestAgentJSONSchemaRejectsTrailingJSON(t *testing.T) {
	var schema JSONSchema
	if err := schema.UnmarshalJSON([]byte(`{"type":"object"} trailing`)); err == nil {
		t.Fatal("expected trailing JSON to be rejected")
	}
}

func TestAgentJSONSchemaPreservesJSONNumberLiterals(t *testing.T) {
	var schema JSONSchema
	const raw = `{"type":"number","minimum":0.12345678901234567890123,"maximum":1e-3}`
	if err := schema.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	got := string(schema)
	if !strings.Contains(got, "0.12345678901234567890123") || !strings.Contains(got, "1e-3") {
		t.Fatalf("schema = %s", got)
	}
}

func TestAgentJSONSchemaInlineYAMLPreservesNumberLiterals(t *testing.T) {
	spec, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      type: number\n      minimum: 0.12345678901234567890123\n      maximum: 1e-3\n"))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := string(*normalized.Agents[0].InputSchema)
	if !strings.Contains(got, "0.12345678901234567890123") || !strings.Contains(got, "1e-3") {
		t.Fatalf("schema = %s", got)
	}
}

func TestAgentJSONSchemaRejectsInvalidSchemaKeywords(t *testing.T) {
	for _, schema := range []string{
		"name: schemas\nagents:\n  worker:\n    input_schema:\n      type: unknown\n",
		"name: schemas\nagents:\n  worker:\n    input_schema:\n      type: string\n      pattern: '[unterminated'\n",
	} {
		spec, err := Parse([]byte(schema))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Normalize(spec, NormalizeOptions{}); err == nil {
			t.Fatalf("expected invalid schema to be rejected: %s", schema)
		}
	}
}

func TestAgentJSONSchemaRejectsExternalReferences(t *testing.T) {
	spec, err := Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      $ref: https://example.test/schema.json\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(spec, NormalizeOptions{}); err == nil || !strings.Contains(err.Error(), "external JSON Schema resource") {
		t.Fatalf("Normalize error = %v", err)
	}
}
