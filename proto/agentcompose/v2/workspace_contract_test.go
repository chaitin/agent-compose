package agentcomposev2

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestWorkspaceWireContract(t *testing.T) {
	fields := []struct {
		name protoreflect.Name
		kind protoreflect.Kind
	}{
		{"provider", protoreflect.StringKind}, {"url", protoreflect.StringKind},
		{"ref", protoreflect.StringKind}, {"path", protoreflect.StringKind},
		{"name", protoreflect.StringKind}, {"format", protoreflect.StringKind},
		{"target", protoreflect.StringKind}, {"username", protoreflect.StringKind},
		{"password", protoreflect.StringKind}, {"token", protoreflect.StringKind},
		{"mode", protoreflect.EnumKind}, {"read_only", protoreflect.BoolKind},
	}
	message := File_agentcompose_v2_agentcompose_proto.Messages().ByName("WorkspaceSpec")
	if message.Fields().Len() != len(fields) {
		t.Fatalf("WorkspaceSpec has %d fields, want %d; review and update the full wire contract", message.Fields().Len(), len(fields))
	}
	for index, want := range fields {
		field := message.Fields().ByNumber(protoreflect.FieldNumber(index + 1))
		if field == nil || field.Name() != want.name || field.Kind() != want.kind || field.Cardinality() != protoreflect.Optional {
			t.Fatalf("WorkspaceSpec field %d = %v, want %s %s", index+1, field, want.name, want.kind)
		}
	}
	mode := message.Fields().ByName("mode").Enum()
	if mode.FullName() != "agentcompose.v2.WorkspaceMode" {
		t.Fatalf("workspace mode enum = %s", mode.FullName())
	}
	wantValues := []protoreflect.Name{"WORKSPACE_MODE_UNSPECIFIED", "WORKSPACE_MODE_COPY", "WORKSPACE_MODE_MOUNT"}
	if mode.Values().Len() != len(wantValues) {
		t.Fatalf("WorkspaceMode has %d values, want %d", mode.Values().Len(), len(wantValues))
	}
	for index, name := range wantValues {
		value := mode.Values().ByNumber(protoreflect.EnumNumber(index))
		if value == nil || value.Name() != name {
			t.Fatalf("WorkspaceMode %d = %v, want %s", index, value, name)
		}
	}
	readonly := message.Fields().ByName("read_only")
	if readonly.HasPresence() || readonly.JSONName() != "readOnly" {
		t.Fatalf("read_only must remain an ordinary bool with JSON name readOnly: %v", readonly)
	}
}

func TestSandboxWorkspaceDeliveryWireContract(t *testing.T) {
	fields := []struct {
		name     protoreflect.Name
		jsonName string
		kind     protoreflect.Kind
	}{
		{"mode", "mode", protoreflect.EnumKind}, {"source_path", "sourcePath", protoreflect.StringKind},
		{"target", "target", protoreflect.StringKind}, {"read_only", "readOnly", protoreflect.BoolKind},
	}
	message := File_agentcompose_v2_agentcompose_proto.Messages().ByName("SandboxWorkspaceDelivery")
	if message.Fields().Len() != len(fields) {
		t.Fatalf("SandboxWorkspaceDelivery has %d fields, want %d; review the entire public metadata surface", message.Fields().Len(), len(fields))
	}
	for index, want := range fields {
		field := message.Fields().ByNumber(protoreflect.FieldNumber(index + 1))
		if field == nil || field.Name() != want.name || field.JSONName() != want.jsonName || field.Kind() != want.kind || field.Cardinality() != protoreflect.Optional || field.HasPresence() {
			t.Fatalf("SandboxWorkspaceDelivery field %d = %v, want ordinary %s %s", index+1, field, want.name, want.kind)
		}
	}
	if mode := message.Fields().ByName("mode").Enum().FullName(); mode != "agentcompose.v2.WorkspaceMode" {
		t.Fatalf("delivery mode enum = %s", mode)
	}
	sandbox := File_agentcompose_v2_agentcompose_proto.Messages().ByName("Sandbox")
	field := sandbox.Fields().ByNumber(25)
	if field == nil || field.Name() != "workspace_delivery" || field.JSONName() != "workspaceDelivery" || field.Kind() != protoreflect.MessageKind || field.Cardinality() != protoreflect.Optional || field.Message().FullName() != message.FullName() || !field.HasPresence() {
		t.Fatalf("Sandbox field 25 must remain optional workspace_delivery: %v", field)
	}
}
