package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestDaemonHTTPRouteAllowlist(t *testing.T) {
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()

	allowed := map[string]string{
		"GET /api/version":                                                       "bootstrap",
		"GET /api/null":                                                          "bootstrap",
		"POST /api/webhooks/:topic":                                              "webhook ingress",
		"POST /api/webhooks/events/:event_id/stop":                               "webhook event cancellation",
		"GET /api/webhook-sources":                                               "webhook administration and event queries",
		"PUT /api/webhook-sources/:source_id":                                    "webhook administration and event queries",
		"DELETE /api/webhook-sources/:source_id":                                 "webhook administration and event queries",
		"GET /api/events":                                                        "webhook administration and event queries",
		"GET /api/events/topics":                                                 "webhook administration and event queries",
		"GET /api/events/:event_id":                                              "webhook administration and event queries",
		"GET /api/events/:event_id/sessions":                                     "webhook administration and event queries",
		"GET /api/events/:event_id/sandboxes":                                    "webhook administration and event queries",
		"GET /api/events/:event_id/runs":                                         "webhook administration and event queries",
		"GET /api/events/:event_id/trace":                                        "webhook administration and event queries",
		"GET /api/agent-compose/workspaces/:workspaceID/files":                   "workspace data plane",
		"POST /api/agent-compose/workspaces/:workspaceID/upload":                 "workspace data plane",
		"GET /api/agent-compose/workspaces/:workspaceID/download":                "workspace data plane",
		"GET /jupyter/:sessionID":                                                "Jupyter proxy",
		"ANY /jupyter/:sessionID/*":                                              "Jupyter proxy",
		"POST /api/runtime/sandboxes/:sandbox_id/llm/openai/v1/responses":        "runtime provider-compatible LLM facade",
		"POST /api/runtime/sandboxes/:sandbox_id/llm/openai/v1/chat/completions": "runtime provider-compatible LLM facade",
		"POST /api/runtime/sandboxes/:sandbox_id/llm/anthropic/v1/messages":      "runtime provider-compatible LLM facade",
	}

	var unexpected []string
	seen := make(map[string]bool, len(allowed))
	for _, route := range app.Echo.Routes() {
		if strings.HasPrefix(route.Path, "/agentcompose.v2.") ||
			strings.HasPrefix(route.Path, "/health.v1.") {
			continue
		}
		if route.Path == "/jupyter/:sessionID/*" {
			seen["ANY "+route.Path] = true
			continue
		}
		key := route.Method + " " + route.Path
		if _, ok := allowed[key]; !ok {
			unexpected = append(unexpected, key)
			continue
		}
		seen[key] = true
	}
	var missing []string
	for route := range allowed {
		if !seen[route] {
			missing = append(missing, route)
		}
	}
	sort.Strings(unexpected)
	sort.Strings(missing)
	if len(unexpected) > 0 || len(missing) > 0 {
		t.Fatalf("HTTP route allowlist drifted; unexpected=%v missing=%v", unexpected, missing)
	}
}

// TestDaemonV2RPCInventoryReachesConcreteHandlers is intentionally driven by
// the protobuf descriptor. Adding an RPC without registering a concrete daemon
// handler therefore fails this test without requiring a second hand-maintained
// list of protocol methods.
func TestDaemonV2RPCInventoryReachesConcreteHandlers(t *testing.T) {
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()

	server := httptest.NewServer(app.Echo)
	defer server.Close()

	services := agentcomposev2.File_agentcompose_v2_agentcompose_proto.Services()
	methodCount := 0
	for serviceIndex := 0; serviceIndex < services.Len(); serviceIndex++ {
		service := services.Get(serviceIndex)
		servicePath := "/" + string(service.FullName()) + "/"
		if !hasRoute(app, "POST", servicePath+"*") {
			t.Errorf("Connect route %s* is not registered", servicePath)
		}

		methods := service.Methods()
		for methodIndex := 0; methodIndex < methods.Len(); methodIndex++ {
			method := methods.Get(methodIndex)
			methodCount++
			t.Run(string(method.FullName()), func(t *testing.T) {
				t.Parallel()
				assertRPCReachesConcreteHandler(t, server, method)
			})
		}
	}

	if services.Len() == 0 || methodCount == 0 {
		t.Fatalf("empty v2 descriptor inventory: services=%d methods=%d", services.Len(), methodCount)
	}
	t.Logf("verified descriptor inventory: services=%d methods=%d", services.Len(), methodCount)
}

func assertRPCReachesConcreteHandler(t *testing.T, server *httptest.Server, method protoreflect.MethodDescriptor) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	procedure := "/" + string(method.FullName())
	client := connect.NewClient[dynamicpb.Message, dynamicpb.Message](
		server.Client(),
		server.URL+procedure,
		connect.WithSchema(method),
	)
	request := dynamicpb.NewMessage(method.Input())

	var err error
	switch {
	case method.IsStreamingClient():
		stream := client.CallBidiStream(ctx)
		if sendErr := stream.Send(request); sendErr != nil {
			err = sendErr
			break
		}
		if closeErr := stream.CloseRequest(); closeErr != nil {
			err = closeErr
			break
		}
		_, err = stream.Receive()
		_ = stream.CloseResponse()
	case method.IsStreamingServer():
		stream, callErr := client.CallServerStream(ctx, connect.NewRequest(request))
		if callErr != nil {
			err = callErr
			break
		}
		if !stream.Receive() {
			err = stream.Err()
		}
		_ = stream.Close()
	default:
		_, err = client.CallUnary(ctx, connect.NewRequest(request))
	}

	defaultFallback := fmt.Sprintf("%s is not implemented", strings.TrimPrefix(procedure, "/"))
	if connect.CodeOf(err) == connect.CodeUnimplemented && strings.Contains(err.Error(), defaultFallback) {
		t.Fatalf("RPC reached generated Unimplemented fallback: %v", err)
	}
}
