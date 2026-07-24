package capproxy

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type recordingTargetResolver struct {
	target      Target
	err         error
	calls       int
	binding     SandboxBinding
	declaration string
}

func (r *recordingTargetResolver) ResolveCapabilityTarget(_ context.Context, binding SandboxBinding, declaration string) (Target, error) {
	r.calls++
	r.binding = binding
	r.declaration = declaration
	return r.target, r.err
}

func TestQualifiedCapsetRoutesBusinessCallWithoutLeakingQualifier(t *testing.T) {
	var received metadata.MD
	octoAddr, stopOcto := startTestRawGRPC(t, func(_ any, stream grpc.ServerStream) error {
		received, _ = metadata.FromIncomingContext(stream.Context())
		request := rawFrame(nil)
		if err := stream.RecvMsg(&request); err != nil {
			return err
		}
		return stream.SendMsg(rawFrame("ok"))
	})
	defer stopOcto()

	targets := &recordingTargetResolver{target: Target{Addr: octoAddr, Token: "project-token", CapsetID: "dev"}}
	binding := SandboxBinding{SandboxID: "s1", CapsetIDs: []string{"internal/dev"}}
	proxyAddr, stopProxy := startTestProxy(t, Config{
		Listen:  "127.0.0.1:0",
		OctoBus: staticOctoBus("unused:1", "global-token"),
		Targets: targets,
	}, testResolver{binding: binding})
	defer stopProxy()

	invokeRaw(t, proxyAddr, "/pkg.Service/Call", metadata.Pairs(
		SandboxTokenMetadata, "sandbox-token",
		"x-octobus-capset", "internal/dev",
		"x-octobus-instance", "instance-1",
		"authorization", "Bearer guest-token",
	))

	if targets.calls != 1 || targets.declaration != "internal/dev" {
		t.Fatalf("resolver calls = %d, declaration = %q", targets.calls, targets.declaration)
	}
	if targets.binding.SandboxID != binding.SandboxID {
		t.Fatalf("resolver binding = %#v, want %#v", targets.binding, binding)
	}
	for key, want := range map[string]string{
		"x-octobus-capset":   "dev",
		"x-octobus-instance": "instance-1",
		"authorization":      "Bearer project-token",
	} {
		if got := firstMetadata(received, key); got != want {
			t.Fatalf("upstream metadata %s = %q, want %q", key, got, want)
		}
	}
	if got := firstMetadata(received, SandboxTokenMetadata); got != "" {
		t.Fatalf("sandbox token leaked upstream: %q", got)
	}
}

func TestQualifiedCapsetRoutesReflectionWithoutInstance(t *testing.T) {
	var received metadata.MD
	octoAddr, stopOcto := startTestRawGRPC(t, func(_ any, stream grpc.ServerStream) error {
		received, _ = metadata.FromIncomingContext(stream.Context())
		request := rawFrame(nil)
		if err := stream.RecvMsg(&request); err != nil {
			return err
		}
		return stream.SendMsg(rawFrame("ok"))
	})
	defer stopOcto()

	targets := &recordingTargetResolver{target: Target{Addr: octoAddr, CapsetID: "catalog"}}
	proxyAddr, stopProxy := startTestProxy(t, Config{
		Listen:  "127.0.0.1:0",
		OctoBus: staticOctoBus("unused:1", ""),
		Targets: targets,
	}, testResolver{binding: SandboxBinding{SandboxID: "s1", CapsetIDs: []string{"internal/catalog"}}})
	defer stopProxy()

	invokeRaw(t, proxyAddr, "/grpc.reflection.v1.ServerReflection/ServerReflectionInfo", metadata.Pairs(
		SandboxTokenMetadata, "sandbox-token",
		"x-octobus-capset", "internal/catalog",
	))
	if got := firstMetadata(received, "x-octobus-capset"); got != "catalog" {
		t.Fatalf("upstream capset = %q, want catalog", got)
	}
}

func TestDisallowedQualifiedCapsetDoesNotResolveTarget(t *testing.T) {
	targets := &recordingTargetResolver{target: Target{Addr: "unused:1", CapsetID: "other"}}
	proxyAddr, stopProxy := startTestProxy(t, Config{
		Listen:  "127.0.0.1:0",
		OctoBus: staticOctoBus("unused:1", ""),
		Targets: targets,
	}, testResolver{binding: SandboxBinding{SandboxID: "s1", CapsetIDs: []string{"internal/dev"}}})
	defer stopProxy()

	err := invokeRawError(t, proxyAddr, "/pkg.Service/Call", metadata.Pairs(
		SandboxTokenMetadata, "sandbox-token",
		"x-octobus-capset", "other/dev",
		"x-octobus-instance", "instance-1",
	))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %s, want PermissionDenied; err=%v", status.Code(err), err)
	}
	if targets.calls != 0 {
		t.Fatalf("target resolver called %d times for disallowed declaration", targets.calls)
	}
}

func TestQualifiedTargetResolutionErrors(t *testing.T) {
	binding := SandboxBinding{SandboxID: "s1", CapsetIDs: []string{"internal/dev"}}
	tests := []struct {
		name    string
		targets TargetResolver
		code    codes.Code
	}{
		{name: "missing resolver", code: codes.Unavailable},
		{name: "resolver error", targets: &recordingTargetResolver{err: status.Error(codes.PermissionDenied, "project mismatch")}, code: codes.PermissionDenied},
		{name: "empty address", targets: &recordingTargetResolver{target: Target{CapsetID: "dev"}}, code: codes.Unavailable},
		{name: "empty capset", targets: &recordingTargetResolver{target: Target{Addr: "upstream:1"}}, code: codes.Unavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := NewServer(Config{OctoBus: staticOctoBus("global:1", ""), Targets: tt.targets}, testResolver{})
			_, err := server.resolveTarget(context.Background(), binding, "internal/dev")
			if status.Code(err) != tt.code {
				t.Fatalf("code = %s, want %s; err=%v", status.Code(err), tt.code, err)
			}
		})
	}
}

func TestUnqualifiedTargetUsesGlobalResolver(t *testing.T) {
	targets := &recordingTargetResolver{err: status.Error(codes.Internal, "must not be called")}
	server := NewServer(Config{OctoBus: staticOctoBus("global:7412", "global-token"), Targets: targets}, testResolver{})
	target, err := server.resolveTarget(context.Background(), SandboxBinding{}, "legacy-capset")
	if err != nil {
		t.Fatal(err)
	}
	if target != (Target{Addr: "global:7412", Token: "global-token", CapsetID: "legacy-capset"}) {
		t.Fatalf("target = %#v", target)
	}
	if targets.calls != 0 {
		t.Fatalf("project resolver called %d times", targets.calls)
	}
}

func invokeRaw(t *testing.T, addr, method string, md metadata.MD) {
	t.Helper()
	if err := invokeRawError(t, addr, method, md); err != nil {
		t.Fatal(err)
	}
}

func invokeRawError(t *testing.T, addr, method string, md metadata.MD) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx := metadata.NewOutgoingContext(context.Background(), md)
	response := rawFrame(nil)
	return conn.Invoke(ctx, method, rawFrame("ping"), &response)
}
