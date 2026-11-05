package capproxy

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestIntegrationProxyReturnsEarlyUpstreamStatusWithoutClientEOF(t *testing.T) {
	stream, _ := openCompletionTestStream(t, func(_ any, _ grpc.ServerStream) error {
		return status.Error(codes.PermissionDenied, "upstream rejected")
	})
	var response rawFrame
	if err := stream.RecvMsg(&response); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("upstream rejection became %s while client send side was open", status.Code(err))
	}
}

func TestIntegrationProxyDrainsResponseAfterClientHalfClose(t *testing.T) {
	stream, _ := openCompletionTestStream(t, func(_ any, upstream grpc.ServerStream) error {
		var request rawFrame
		if err := upstream.RecvMsg(&request); err != nil {
			return err
		}
		if err := upstream.RecvMsg(new(rawFrame)); !errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "request not half closed")
		}
		return upstream.SendMsg(rawFrame("reply:" + string(request)))
	})
	if err := stream.SendMsg(rawFrame("request")); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	var response rawFrame
	if err := stream.RecvMsg(&response); err != nil || string(response) != "reply:request" {
		t.Fatalf("half-close lost response: %q, %v", response, err)
	}
	if err := stream.RecvMsg(new(rawFrame)); !errors.Is(err, io.EOF) {
		t.Fatalf("upstream completion = %v, want EOF", err)
	}
}

func TestIntegrationProxyCancelsActiveUpstreamOnClientCancellation(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	stream, cancel := openCompletionTestStream(t, func(_ any, upstream grpc.ServerStream) error {
		close(started)
		<-upstream.Context().Done()
		close(cancelled)
		return status.FromContextError(upstream.Context().Err()).Err()
	})
	if err := stream.SendMsg(rawFrame("start")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-stream.Context().Done():
		t.Fatal("upstream never started")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("client cancellation did not reach the active upstream")
	}
}

func openCompletionTestStream(t *testing.T, handler grpc.StreamHandler) (grpc.ClientStream, context.CancelFunc) {
	t.Helper()
	upstream, stopUpstream := startTestRawGRPC(t, handler)
	t.Cleanup(stopUpstream)
	proxy, stopProxy := startTestProxy(t, Config{Listen: "127.0.0.1:0", OctoBus: staticOctoBus(upstream, "")}, testResolver{binding: SandboxBinding{SandboxID: "completion", CapsetIDs: []string{"dev"}}})
	t.Cleanup(stopProxy)
	conn, err := grpc.NewClient(proxy, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(SandboxTokenMetadata, "sandbox-token", "x-octobus-capset", "dev", "x-octobus-instance", "completion"))
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, "/test.Service/Call")
	if err != nil {
		t.Fatal(err)
	}
	return stream, cancel
}
