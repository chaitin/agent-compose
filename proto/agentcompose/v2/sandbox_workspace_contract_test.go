package agentcomposev2

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSandboxWorkspaceRPCShape(t *testing.T) {
	t.Parallel()
	service := File_agentcompose_v2_agentcompose_proto.Services().ByName("SandboxService")
	if service == nil {
		t.Fatal("SandboxService not found")
	}
	list := service.Methods().ByName("ListSandboxWorkspace")
	if list == nil || list.Input().FullName() != "agentcompose.v2.ListSandboxWorkspaceRequest" ||
		list.Output().FullName() != "agentcompose.v2.ListSandboxWorkspaceResponse" ||
		list.IsStreamingClient() || list.IsStreamingServer() {
		t.Fatalf("ListSandboxWorkspace shape = %v", list)
	}
	download := service.Methods().ByName("DownloadSandboxWorkspaceFile")
	if download == nil || download.Input().FullName() != "agentcompose.v2.DownloadSandboxWorkspaceFileRequest" ||
		download.Output().FullName() != "agentcompose.v2.DownloadSandboxWorkspaceFileResponse" ||
		download.IsStreamingClient() || !download.IsStreamingServer() {
		t.Fatalf("DownloadSandboxWorkspaceFile shape = %v", download)
	}
}

func TestSandboxWorkspaceMessagesHaveNoGaps(t *testing.T) {
	t.Parallel()
	files := []protoreflect.FileDescriptor{File_agentcompose_v2_sandbox_workspace_proto}
	for _, file := range files {
		assertMessageNumbersHaveNoGaps(t, file.Messages(), nil)
		assertEnumNumbersHaveNoGaps(t, file.Enums())
	}
}

func TestSandboxWorkspaceWireFields(t *testing.T) {
	t.Parallel()
	request := File_agentcompose_v2_sandbox_workspace_proto.Messages().ByName("ListSandboxWorkspaceRequest")
	wantRequest := []protoreflect.Name{"sandbox_id", "path", "offset", "limit"}
	assertMessageFields(t, request, wantRequest)
	response := File_agentcompose_v2_sandbox_workspace_proto.Messages().ByName("ListSandboxWorkspaceResponse")
	assertMessageFields(t, response, []protoreflect.Name{"entries", "total"})
	download := File_agentcompose_v2_sandbox_workspace_proto.Messages().ByName("DownloadSandboxWorkspaceFileResponse")
	oneof := download.Oneofs().ByName("payload")
	if oneof == nil || oneof.Fields().Len() != 2 ||
		oneof.Fields().ByNumber(1).Name() != "info" || oneof.Fields().ByNumber(2).Name() != "chunk" {
		t.Fatalf("download payload oneof = %v", oneof)
	}
}

func assertMessageFields(t *testing.T, message protoreflect.MessageDescriptor, want []protoreflect.Name) {
	t.Helper()
	if message == nil {
		t.Fatal("message not found")
	}
	if message.Fields().Len() != len(want) {
		t.Fatalf("%s has %d fields, want %d", message.FullName(), message.Fields().Len(), len(want))
	}
	for index, name := range want {
		field := message.Fields().ByNumber(protoreflect.FieldNumber(index + 1))
		if field == nil || field.Name() != name {
			t.Fatalf("%s field %d = %v, want %s", message.FullName(), index+1, field, name)
		}
	}
}
