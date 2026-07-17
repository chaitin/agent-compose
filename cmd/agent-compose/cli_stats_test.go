package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIStatsTableAndJSON(t *testing.T) {
	var calls int
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			getStats: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
				calls++
				if req.Msg.GetSandboxId() != "sandbox-stats" {
					t.Fatalf("GetSandboxStats sandbox = %q", req.Msg.GetSandboxId())
				}
				return connect.NewResponse(&agentcomposev2.GetSandboxStatsResponse{Stats: &agentcomposev2.SandboxStats{
					SandboxId:        req.Msg.GetSandboxId(),
					Driver:           "docker",
					SampledAt:        "2026-07-04T08:00:00Z",
					CpuPercent:       testStatsMetric(12.5, "percent"),
					MemoryUsageBytes: testStatsMetric(512, "bytes"),
					MemoryLimitBytes: &agentcomposev2.MetricValue{Unit: "bytes", Status: agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN, Message: "missing"},
					MemoryPercent:    testStatsMetric(25, "percent"),
					NetworkRxBytes:   testStatsMetric(100, "bytes"),
					NetworkTxBytes:   testStatsMetric(200, "bytes"),
					BlockReadBytes:   testStatsMetric(300, "bytes"),
					BlockWriteBytes:  testStatsMetric(400, "bytes"),
					UptimeSeconds:    testStatsMetric(90, "seconds"),
				}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stats", "--host", server.URL, "sandbox-stats")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("stats code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"SANDBOX", "sandbox-stat", "docker", "12.50", "512", "-", "90s"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stats output %q does not contain %q", stdout, want)
		}
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("stats", "--host", server.URL, "--json", "sandbox-stats")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("stats --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeStatsOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("stats JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.SandboxID != "sandbox-stats" || decoded.Driver != "docker" || decoded.MemoryLimitBytes.Status != "unknown" || decoded.MemoryLimitBytes.Value != nil {
		t.Fatalf("stats JSON = %#v", decoded)
	}
	if decoded.CPUPercent.Value == nil || *decoded.CPUPercent.Value != 12.5 {
		t.Fatalf("stats JSON cpu = %#v", decoded.CPUPercent)
	}
	if calls != 2 {
		t.Fatalf("GetSandboxStats calls = %d, want 2", calls)
	}
}

func TestCLIStatsUnsupportedUsesUnsupportedExitCode(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			getStats: func(context.Context, *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
				return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("sandbox stats are unsupported"))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("stats", "--host", server.URL, "sandbox-stats")
	if exitCode != exitCodeUnsupported {
		t.Fatalf("stats unsupported exit code = %d, want %d; stderr=%q", exitCode, exitCodeUnsupported, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "unsupported") {
		t.Fatalf("stats unsupported stdout/stderr = %q / %q", stdout, stderr)
	}
}

func testStatsMetric(value float64, unit string) *agentcomposev2.MetricValue {
	return &agentcomposev2.MetricValue{Value: &value, Unit: unit, Status: agentcomposev2.MetricStatus_METRIC_STATUS_OK}
}
