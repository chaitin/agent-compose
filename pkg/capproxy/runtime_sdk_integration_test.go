package capproxy

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type revocableSDKResolver struct{ revoked atomic.Bool }

func (r *revocableSDKResolver) ResolveCapabilitySandbox(_ context.Context, token string) (SandboxBinding, error) {
	if r.revoked.Load() || token != "sandbox-token" {
		return SandboxBinding{}, status.Error(codes.Unauthenticated, "credential revoked")
	}
	return SandboxBinding{SandboxID: "sdk-sandbox", CapsetIDs: []string{"dev"}}, nil
}

// Opt-in: real Node, Codex SDK subprocess, grpcurl, reflection and capproxy;
// the upstream is a local gRPC health service, not an OB deployment or a model.
func TestIntegrationRuntimeSDKThroughExistingFakeCodexAndCapabilityProxy(t *testing.T) {
	if os.Getenv("AC_RUNNER_OB_TEST") != "1" {
		t.Skip("set AC_RUNNER_OB_TEST=1 after building runtime/javascript and the Runtime SDK")
	}
	for _, binary := range []string{"node", "grpcurl"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		if firstMetadata(md, "authorization") != "Bearer upstream-only" || firstMetadata(md, SandboxTokenMetadata) != "" || firstMetadata(md, "x-octobus-capset") != "dev" {
			return nil, status.Error(codes.PermissionDenied, "upstream metadata mismatch")
		}
		request := req.(*healthpb.HealthCheckRequest)
		if firstMetadata(md, "x-octobus-instance") != request.Service {
			return nil, status.Error(codes.PermissionDenied, "instance mismatch")
		}
		calls.Add(1)
		return handler(ctx, req)
	}))
	healthServer := health.NewServer()
	healthServer.SetServingStatus("first", healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus("second", healthpb.HealthCheckResponse_NOT_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	reflection.Register(server)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-serveDone; err != nil {
			t.Errorf("upstream server: %v", err)
		}
	})
	resolver := &revocableSDKResolver{}
	proxyAddr, stop := startTestProxy(t, Config{Listen: "127.0.0.1:0", OctoBus: staticOctoBus(listener.Addr().String(), "upstream-only")}, resolver)
	t.Cleanup(stop)
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	if err := os.MkdirAll(filepath.Join(runtimeRoot, "mpi"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSDKCatalog(t, runtimeRoot, "dev")
	for _, test := range []struct{ instance, expected string }{{"first", "SERVING"}, {"second", "NOT_SERVING"}} {
		t.Run(test.instance, func(t *testing.T) {
			output, err := runSDKRunner(t, root, runtimeRoot, proxyAddr, test.instance)
			if err != nil {
				t.Fatalf("runner: %v\n%s", err, output)
			}
			var response struct {
				Result struct {
					FinalText       string `json:"finalText"`
					FinalTextSource string `json:"finalTextSource"`
				} `json:"result"`
				Events []map[string]any `json:"events"`
			}
			if err := json.Unmarshal(output, &response); err != nil {
				t.Fatal(err)
			}
			if response.Result.FinalTextSource != "provider_message" || !strings.Contains(response.Result.FinalText, `"status":"`+test.expected+`"`) {
				t.Fatalf("runner result = %s", output)
			}
			for _, event := range response.Events {
				if event["kind"] == "usage" {
					t.Fatal("fixture fabricated model usage")
				}
			}
		})
	}
	if calls.Load() != 2 {
		t.Fatalf("business calls = %d, want 2", calls.Load())
	}
	// A forged catalog cannot grant another capset at the proxy boundary.
	writeSDKCatalog(t, runtimeRoot, "forbidden")
	if output, err := runSDKRunner(t, root, runtimeRoot, proxyAddr, "first"); err == nil || strings.Contains(string(output), "sandbox-token") {
		t.Fatalf("unauthorized runner err=%v output=%s", err, output)
	}
	if calls.Load() != 2 {
		t.Fatal("unauthorized call reached upstream")
	}
	writeSDKCatalog(t, runtimeRoot, "dev")
	resolver.revoked.Store(true)
	if output, err := runSDKRunner(t, root, runtimeRoot, proxyAddr, "first"); err == nil || strings.Contains(string(output), "upstream-only") {
		t.Fatalf("revoked runner err=%v output=%s", err, output)
	}
	conn, err := grpc.NewClient(proxyAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(SandboxTokenMetadata, "sandbox-token", "x-octobus-capset", "dev", "x-octobus-instance", "first"))
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: "first"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked token status = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatal("revoked credential reached upstream")
	}
}

func writeSDKCatalog(t *testing.T, root, capset string) {
	t.Helper()
	tools := []map[string]string{}
	for i, instance := range []string{"first", "second"} {
		tools = append(tools, map[string]string{
			"id": strings.Repeat([]string{"a", "b"}[i], 64), "capset": capset, "instance": instance,
			"method": "grpc.health.v1.Health/Check", "request_type": "grpc.health.v1.HealthCheckRequest", "response_type": "grpc.health.v1.HealthCheckResponse",
		})
	}
	content, err := json.Marshal(map[string]any{"version": 1, "sandbox_id": "sdk-sandbox", "tools": tools, "unavailable_capsets": []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mpi", "capabilities.json"), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func runSDKRunner(t *testing.T, root, runtimeRoot, proxyAddr, instance string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "testdata/capabilities-runner-host.mjs", instance)
	command.Env = append(os.Environ(), "AC_TEST_STATE="+root, "RUNTIME_ROOT="+runtimeRoot, "SANDBOX_ID=sdk-sandbox", "CAP_TOKEN=sandbox-token", "CAP_GRPC_TARGET="+proxyAddr)
	return command.CombinedOutput()
}
