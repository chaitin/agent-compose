package api

import (
	"testing"

	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"

	"gopkg.in/yaml.v3"
)

func TestAgentSpecsToProtoIncludesJSONSchemas(t *testing.T) {
	input := compose.JSONSchema(`{"type":"object"}`)
	output := compose.JSONSchema(`false`)
	items := AgentSpecsToProto([]compose.NormalizedAgentSpec{{
		Name:         "worker",
		InputSchema:  &input,
		OutputSchema: &output,
	}})
	if len(items) != 1 || items[0].InputSchemaJson != `{"type":"object"}` || items[0].OutputSchemaJson != "false" {
		t.Fatalf("mapped agent = %#v", items)
	}
}

func TestProjectSpecSchemaProtoRoundTripPreservesHash(t *testing.T) {
	parsedOriginal, err := compose.Parse([]byte("name: schemas\nagents:\n  worker:\n    input_schema:\n      type: object\n      provider: custom-keyword\n      properties:\n        count:\n          type: integer\n          default: 9223372036854775807\n    output_schema:\n      type: string\n"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := compose.Normalize(parsedOriginal, compose.NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantHash, err := original.Hash()
	if err != nil {
		t.Fatal(err)
	}
	shape, issues := ProjectSpecYAMLShape(ProjectSpecToProto(original))
	if len(issues) != 0 {
		t.Fatalf("ProjectSpecYAMLShape issues = %#v", issues)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := compose.Normalize(parsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gotHash, err := roundTrip.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != wantHash {
		t.Fatalf("round-trip hash = %s, want %s\nshape:\n%s", gotHash, wantHash, data)
	}
}

func TestAgentYAMLMapRestoresJSONSchemas(t *testing.T) {
	input := compose.JSONSchema(`{"type":"object"}`)
	output := compose.JSONSchema(`false`)
	protoAgents := AgentSpecsToProto([]compose.NormalizedAgentSpec{{Name: "worker", InputSchema: &input, OutputSchema: &output}})
	agents, issues := AgentYAMLMap(protoAgents)
	if len(issues) != 0 {
		t.Fatalf("AgentYAMLMap issues = %#v", issues)
	}
	worker, ok := agents["worker"].(map[string]any)
	if !ok || worker["input_schema"] == nil || worker["output_schema"] != false {
		t.Fatalf("restored agent = %#v", agents["worker"])
	}
}

func TestAgentYAMLMapRejectsInvalidJSONSchema(t *testing.T) {
	_, issues := AgentYAMLMap([]*agentcomposev2.AgentSpec{{Name: "worker", InputSchemaJson: "[]"}})
	if len(issues) != 1 || issues[0].GetPath() != "agents[0].input_schema_json" {
		t.Fatalf("issues = %#v", issues)
	}
}
