package capproxy

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestIntegrationRuntimeSDKScalarProtoJSON(t *testing.T) {
	if os.Getenv("AC_RUNNER_OB_TEST") != "1" {
		t.Skip("requires built Runtime SDK, Node and grpcurl")
	}
	upstream := startScalarSDKServer(t)
	address, stop := startTestProxy(t, Config{Listen: "127.0.0.1:0", OctoBus: staticOctoBus(upstream, "upstream-only")}, &revocableSDKResolver{})
	t.Cleanup(stop)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "mpi"), 0o755); err != nil {
		t.Fatal(err)
	}
	tool := map[string]string{"id": strings.Repeat("a", 64), "capset": "dev", "instance": "scalar", "method": "scalar.Service/Echo", "request_type": "google.protobuf.StringValue", "response_type": "google.protobuf.StringValue"}
	data, err := json.Marshal(map[string]any{"version": 1, "sandbox_id": "sdk-sandbox", "tools": []any{tool}, "unavailable_capsets": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mpi/capabilities.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	script := `import {capabilities} from './runtime/agent-compose-runtime-sdk/dist/esm/index.js';
const result=await capabilities.call('a'.repeat(64),'standard-string-value');
if(result!=='standard-string-value')throw Error('scalar response mismatch');console.log('scalar-round-trip-ok');`
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", script)
	command.Dir = "../.."
	command.Env = append(os.Environ(), "RUNTIME_ROOT="+root, "SANDBOX_ID=sdk-sandbox", "CAP_TOKEN=sandbox-token", "CAP_GRPC_TARGET="+address)
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "scalar-round-trip-ok") {
		t.Fatalf("real Protobuf scalar round trip failed: %v, %s", err, output)
	}
}

func startScalarSDKServer(t *testing.T) string {
	t.Helper()
	files := new(protoregistry.Files)
	if err := files.RegisterFile(wrapperspb.File_google_protobuf_wrappers_proto); err != nil {
		t.Fatal(err)
	}
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name: proto.String("scalar.proto"), Package: proto.String("scalar"), Syntax: proto.String("proto3"), Dependency: []string{"google/protobuf/wrappers.proto"},
		Service: []*descriptorpb.ServiceDescriptorProto{{Name: proto.String("Service"), Method: []*descriptorpb.MethodDescriptorProto{{
			Name: proto.String("Echo"), InputType: proto.String(".google.protobuf.StringValue"), OutputType: proto.String(".google.protobuf.StringValue"),
		}}}},
	}, files)
	if err != nil {
		t.Fatal(err)
	}
	if err := files.RegisterFile(file); err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "scalar.Service", Metadata: "scalar.proto", Methods: []grpc.MethodDesc{{MethodName: "Echo", Handler: func(_ any, _ context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		request := new(wrapperspb.StringValue)
		if err := decode(request); err != nil {
			return nil, err
		}
		return request, nil
	}}}}, nil)
	// Only v1alpha: the real client must receive Unimplemented for v1 and fall back.
	reflectionpb.RegisterServerReflectionServer(server, reflection.NewServer(reflection.ServerOptions{Services: server, DescriptorResolver: files}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return listener.Addr().String()
}
