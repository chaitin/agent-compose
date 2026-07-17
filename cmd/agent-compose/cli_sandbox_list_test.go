package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestWaitForDetachedRunSandboxStopsOnTerminalRun(t *testing.T) {
	client := runServiceStub{
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "", agentcomposev2.RunStatus_RUN_STATUS_FAILED, 1, "")}), nil
		},
	}
	run, err := waitForDetachedRunSandbox(context.Background(), client, "project-detached", "run-terminal", time.Second)
	if err == nil || !strings.Contains(err.Error(), "completed before reporting a sandbox") {
		t.Fatalf("waitForDetachedRunSandbox err = %v", err)
	}
	if run == nil || run.GetRunId() != "run-terminal" {
		t.Fatalf("waitForDetachedRunSandbox run = %#v", run)
	}
}

func (s sandboxServiceStub) StopSandbox(ctx context.Context, req *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error) {
	if s.stopSandbox == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("StopSandbox stub is not configured"))
	}
	return s.stopSandbox(ctx, req)
}

func (s sandboxServiceStub) GetSandboxStats(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error) {
	if s.getStats == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetSandboxStats stub is not configured"))
	}
	return s.getStats(ctx, req)
}

type sessionServiceStub struct {
	getSession      func(context.Context, *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error)
	getSessionProxy func(context.Context, *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error)
	listSessions    func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error)
	resumeSession   func(context.Context, *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error)
	stopSession     func(context.Context, *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error)
}

func TestResolveComposeSandboxRefFromSessions(t *testing.T) {
	testResolveComposeSandboxRefFromSessions(t)
}

func testResolveComposeSandboxRefFromSessions(t *testing.T) {
	const (
		sandboxID      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
		otherSandboxID = "0123456789abffff0123456789abcdef0123456789abcdef0123456789abcdef"
	)
	tests := []struct {
		name       string
		ref        string
		sessions   []*agentcomposev2.Sandbox
		listErr    error
		want       string
		wantCode   int
		wantErrors []string
	}{
		{
			name:     "short id",
			ref:      sandboxID[:12],
			sessions: []*agentcomposev2.Sandbox{{SandboxId: sandboxID}},
			want:     sandboxID,
		},
		{
			name:     "full id with empty sessions ignored",
			ref:      sandboxID,
			sessions: []*agentcomposev2.Sandbox{nil, {}, {SandboxId: "   "}, {SandboxId: " " + sandboxID + " "}},
			want:     sandboxID,
		},
		{
			name:       "not found",
			ref:        "deadbeef",
			sessions:   []*agentcomposev2.Sandbox{{SandboxId: sandboxID}},
			wantCode:   exitCodeUsage,
			wantErrors: []string{`sandbox "deadbeef" not found in daemon sessions`},
		},
		{
			name:       "ambiguous short id",
			ref:        sandboxID[:12],
			sessions:   []*agentcomposev2.Sandbox{{SandboxId: otherSandboxID}, {SandboxId: sandboxID}},
			wantCode:   exitCodeUsage,
			wantErrors: []string{"is ambiguous", sandboxID[:12] + ", " + otherSandboxID[:12]},
		},
		{
			name:       "list error",
			ref:        sandboxID[:12],
			listErr:    connect.NewError(connect.CodeUnavailable, fmt.Errorf("daemon unavailable")),
			wantCode:   exitCodeUnavailable,
			wantErrors: []string{"resolve sandbox " + sandboxID[:12] + " from daemon sessions", "daemon unavailable"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := newComposeServiceStubServer(t, composeServiceStubs{sandbox: sandboxServiceStub{
				listSandboxes: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
					if tc.listErr != nil {
						return nil, tc.listErr
					}
					return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: tc.sessions}), nil
				},
			}})
			defer server.Close()
			client := agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
			got, err := resolveComposeSandboxRefFromSessions(context.Background(), client, tc.ref)
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("resolve sandbox ref: %v", err)
				}
				if got != tc.want {
					t.Fatalf("resolved sandbox id = %q, want %q", got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatal("resolve sandbox ref returned nil error")
			}
			if got != "" {
				t.Fatalf("resolved sandbox id = %q, want empty", got)
			}
			if code := commandExitCode(err); code != tc.wantCode {
				t.Fatalf("exit code = %d, want %d; err=%v", code, tc.wantCode, err)
			}
			for _, want := range tc.wantErrors {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func sandboxStubWithSessionCompatibility(sandbox sandboxServiceStub, session sessionServiceStub) sandboxServiceStub {
	if sandbox.getSandbox == nil && (session.getSession != nil || session.getSessionProxy != nil) {
		sandbox.getSandbox = func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
			if session.getSession != nil {
				resp, err := session.getSession(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: req.Msg.GetSandboxId()}))
				if err != nil {
					return nil, err
				}
				return resp, nil
			}
			resp, err := session.getSessionProxy(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: req.Msg.GetSandboxId()}))
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
	}
	if sandbox.listSandboxes == nil && session.listSessions != nil {
		sandbox.listSandboxes = func(ctx context.Context, _ *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
			resp, err := session.listSessions(ctx, connect.NewRequest(&agentcomposev2.ListSandboxesRequest{Limit: 100}))
			if err != nil {
				return nil, err
			}
			return resp, nil
		}
	}
	if sandbox.stopSandbox == nil && session.stopSession != nil {
		sandbox.stopSandbox = func(ctx context.Context, req *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error) {
			return session.stopSession(ctx, req)
		}
	}
	if sandbox.resumeSandbox == nil && session.resumeSession != nil {
		sandbox.resumeSandbox = func(ctx context.Context, req *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error) {
			return session.resumeSession(ctx, req)
		}
	}
	return sandbox
}

func testCLISessionDetail(sessionID, vmStatus string) *agentcomposev2.Sandbox {
	return testCLISessionSummary(sessionID, vmStatus, "project-cli", "reviewer", "")
}

func testCLISessionSummary(sessionID, vmStatus, projectID, agentName, runID string) *agentcomposev2.Sandbox {
	tags := []*agentcomposev2.SandboxTag{{Name: "project", Value: projectID}}
	if agentName != "" {
		tags = append(tags, &agentcomposev2.SandboxTag{Name: "agent", Value: agentName})
	}
	if runID != "" {
		tags = append(tags, &agentcomposev2.SandboxTag{Name: "run_id", Value: runID})
	}
	return &agentcomposev2.Sandbox{
		SandboxId:     sessionID,
		Title:         "CLI Session",
		Driver:        "boxlite",
		Status:        vmStatus,
		WorkspacePath: "/workspace/" + sessionID,
		ProxyPath:     "/agent-compose/session/" + sessionID + "/lab",
		Image:         "guest:latest",
		TriggerSource: "manual",
		CreatedAt:     timestamppb.New(time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)),
		UpdatedAt:     timestamppb.New(time.Date(2026, 6, 11, 0, 0, 1, 0, time.UTC)),
		CellCount:     1,
		EventCount:    2,
		Tags:          tags,
	}
}
