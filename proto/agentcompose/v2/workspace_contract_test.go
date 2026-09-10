package agentcomposev2

import (
	"fmt"
	"slices"
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

func TestSandboxFullWireContract(t *testing.T) {
	want := []string{
		"1:sandbox_id:sandboxId:string:optional:false",
		"2:status:status:enum:agentcompose.v2.SandboxStatus:optional:false",
		"3:driver:driver:string:optional:false",
		"4:project_id:projectId:string:optional:false",
		"5:agent_name:agentName:string:optional:false",
		"6:created_at:createdAt:message:google.protobuf.Timestamp:optional:true",
		"7:updated_at:updatedAt:message:google.protobuf.Timestamp:optional:true",
		"8:image:image:string:optional:false",
		"9:workspace_path:workspacePath:string:optional:false",
		"10:tags:tags:message:agentcompose.v2.SandboxTag:repeated:false",
		"11:title:title:string:optional:false",
		"12:proxy_path:proxyPath:string:optional:false",
		"13:trigger_source:triggerSource:string:optional:false",
		"14:cell_count:cellCount:uint32:optional:false",
		"15:event_count:eventCount:uint32:optional:false",
		"16:notebook_url:notebookUrl:string:optional:false",
		"17:workspace_reclamation_state:workspaceReclamationState:enum:agentcompose.v2.WorkspaceReclamationState:optional:false",
		"18:workspace_reclamation_started_at:workspaceReclamationStartedAt:message:google.protobuf.Timestamp:optional:true",
		"19:workspace_reclamation_completed_at:workspaceReclamationCompletedAt:message:google.protobuf.Timestamp:optional:true",
		"20:workspace_reclamation_last_error:workspaceReclamationLastError:string:optional:false",
		"21:stopped_runtime_policy:stoppedRuntimePolicy:string:optional:false",
		"22:stopped_runtime_state:stoppedRuntimeState:string:optional:false",
		"23:stopped_runtime_last_error:stoppedRuntimeLastError:string:optional:false",
		"24:stopped_runtime_released_at:stoppedRuntimeReleasedAt:message:google.protobuf.Timestamp:optional:true",
		"25:workspace_delivery:workspaceDelivery:message:agentcompose.v2.SandboxWorkspaceDelivery:optional:true",
	}
	fields := (&Sandbox{}).ProtoReflect().Descriptor().Fields()
	got := make([]string, fields.Len())
	for index := range fields.Len() {
		field := fields.Get(index)
		fieldType := field.Kind().String()
		if field.Kind() == protoreflect.MessageKind {
			fieldType += ":" + string(field.Message().FullName())
		} else if field.Kind() == protoreflect.EnumKind {
			fieldType += ":" + string(field.Enum().FullName())
		}
		got[index] = fmt.Sprintf("%d:%s:%s:%s:%s:%t", field.Number(), field.Name(), field.JSONName(), fieldType, field.Cardinality(), field.HasPresence())
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Sandbox wire contract changed; review every field before updating the golden\ngot: %q\nwant: %q", got, want)
	}
}
