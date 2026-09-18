package agentcomposev2

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSchedulerScriptSourceWireContract(t *testing.T) {
	fields := (&SchedulerSpec{}).ProtoReflect().Descriptor().Fields()
	script := fields.ByName("script")
	source := fields.ByName("script_source")
	if script.Number() != 3 || script.Kind() != protoreflect.StringKind || script.ContainingOneof() != nil {
		t.Fatal("legacy script field changed")
	}
	if source.Number() != 10 || source.Kind() != protoreflect.MessageKind || source.Message().Name() != "SchedulerScriptSource" {
		t.Fatal("unexpected source field contract")
	}
	names := []protoreflect.Name{"provider", "url", "ref", "path", "username", "password", "token"}
	sourceFields := source.Message().Fields()
	if sourceFields.Len() != len(names) {
		t.Fatal("unexpected source fields")
	}
	for i, name := range names {
		field := sourceFields.ByNumber(protoreflect.FieldNumber(i + 1))
		if field.Name() != name || field.Kind() != protoreflect.StringKind {
			t.Fatalf("unexpected field %d: %v", i+1, field)
		}
	}
	for _, spec := range []*SchedulerSpec{{Script: "function main() {}"}, {ScriptSource: &SchedulerScriptSource{Provider: "git", Url: "https://example.test/repo", Ref: "main", Path: "scheduler.js"}}} {
		data, err := proto.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		decoded := &SchedulerSpec{}
		if err := proto.Unmarshal(data, decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(spec, decoded) {
			t.Fatal("binary round-trip lost script")
		}
		data, err = protojson.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := protojson.Unmarshal(data, decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(spec, decoded) {
			t.Fatal("JSON round-trip lost script")
		}
	}
}
