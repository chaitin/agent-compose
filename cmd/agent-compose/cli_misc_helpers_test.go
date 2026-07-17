package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestJoinBaseURLAndPathPreservesAbsoluteNotebookURLWithoutHTTPTransport(t *testing.T) {
	want := "https://notebooks.example/agent-compose/session/sandbox/lab?token=absolute-token"
	if got := joinBaseURLAndPath("", want); got != want {
		t.Fatalf("absolute notebook URL = %q, want %q", got, want)
	}
}

func TestParseOlderThanSeconds(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    uint64
		wantErr string
	}{
		{name: "empty", value: "", want: 0},
		{name: "days", value: "7d", want: 7 * 24 * 3600},
		{name: "hours", value: "168h", want: 7 * 24 * 3600},
		{name: "fractional seconds", value: "1500ms", want: 1},
		{name: "invalid", value: "later", wantErr: "expected a positive duration"},
		{name: "zero", value: "0", wantErr: "duration must be positive"},
		{name: "negative", value: "-1h", wantErr: "duration must be positive"},
		{name: "subsecond", value: "500ms", wantErr: "duration must be at least 1s"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOlderThanSeconds(tc.value)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseOlderThanSeconds(%q) error = %v, want %q", tc.value, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOlderThanSeconds(%q) unexpected error: %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("parseOlderThanSeconds(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestCLICommandBranchSweepWorkflows(t *testing.T) {
	t.Run("project pull and build empty or invalid", func(t *testing.T) {
		noImageCompose := writeComposeFile(t, t.TempDir(), `
name: cli-empty-images
agents:
  reviewer:
    provider: codex
`)
		stdout, stderr, _, exitCode := executeCLICommand("pull", "--file", noImageCompose)
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "No project images configured") {
			t.Fatalf("pull no images code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		jsonOut, jsonErr, _, jsonCode := executeCLICommand("pull", "--file", noImageCompose, "--json")
		if jsonCode != 0 || jsonErr != "" || !strings.Contains(jsonOut, `"images": []`) {
			t.Fatalf("pull no images json code/stdout/stderr = %d/%q/%q", jsonCode, jsonOut, jsonErr)
		}
		buildOut, buildErr, _, buildCode := executeCLICommand("build", "--file", noImageCompose)
		if buildCode != 0 || buildErr != "" || !strings.Contains(buildOut, "No project images configured for build") {
			t.Fatalf("build no images code/stdout/stderr = %d/%q/%q", buildCode, buildOut, buildErr)
		}

		buildCompose := writeComposeFile(t, t.TempDir(), `
name: cli-build-invalid
agents:
  reviewer:
    provider: codex
    build:
      context: .
  tagged:
    provider: codex
    image: tagged:latest
    build:
      context: .
`)
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{name: "unknown agent", args: []string{"build", "--file", buildCompose, "missing"}, want: "unknown build agent"},
			{name: "missing tag", args: []string{"build", "--file", buildCompose, "reviewer"}, want: "requires image or build.tags"},
			{name: "bad build arg", args: []string{"build", "--file", buildCompose, "--build-arg", "BROKEN", "tagged"}, want: "invalid --build-arg"},
			{name: "bad platform", args: []string{"build", "--file", buildCompose, "--platform", "linux", "tagged"}, want: "expected os/arch"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
				if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
					t.Fatalf("%v code/stdout/stderr = %d/%q/%q, want %q", tc.args, exitCode, stdout, stderr, tc.want)
				}
			})
		}
	})

	t.Run("exec stream error result branches", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-branches
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			exec: execServiceStub{
				execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
					switch target := req.Msg.GetTarget().(type) {
					case *agentcomposev2.ExecRequest_SandboxId:
						switch target.SandboxId {
						case "no-result":
							return nil
						case "failed":
							return stream.Send(&agentcomposev2.ExecStreamResponse{
								EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
								Result: &agentcomposev2.ExecResult{
									ExecId: "exec-failed", SandboxId: target.SandboxId,
									Command: req.Msg.GetCommand(), ExitCode: 42, Success: false, Stderr: "boom\n",
								},
							})
						case "default-shell":
							if req.Msg.GetCommand().GetCommand() != "sh" || len(req.Msg.GetCommand().GetArgs()) != 0 {
								t.Fatalf("default shell request = %#v", req.Msg.GetCommand())
							}
							return stream.Send(&agentcomposev2.ExecStreamResponse{
								EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
								Result:    &agentcomposev2.ExecResult{ExecId: "exec-shell", SandboxId: target.SandboxId, Command: req.Msg.GetCommand(), Success: true},
							})
						}
					case *agentcomposev2.ExecRequest_RunId:
						if target.RunId == "run-stream-error" {
							return connect.NewError(connect.CodeUnavailable, fmt.Errorf("exec stream down"))
						}
					}
					return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unexpected exec request"))
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "no-result", "--command", "true")
		if exitCode != exitCodeGeneral || stdout != "" || !strings.Contains(stderr, "stream completed without result") {
			t.Fatalf("exec no result code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("exec", "--host", server.URL, "--file", composePath, "failed", "--command", "false")
		if exitCode != 42 || stdout != "" || !strings.Contains(stderr, "exec-failed") || !strings.Contains(stderr, "boom") {
			t.Fatalf("exec failed code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--run", "run-stream-error", "--command", "true")
		if exitCode != exitCodeUnavailable || stdout != "" || !strings.Contains(stderr, "exec stream down") {
			t.Fatalf("exec stream error code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		for _, tc := range []struct {
			args []string
			want string
		}{
			{args: []string{"exec", "--file", composePath, "--run", "", "--command", "true"}, want: "requires a value"},
			{args: []string{"exec", "--file", composePath, "--agent", "", "true"}, want: "unknown flag: --agent"},
		} {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v code/stdout/stderr = %d/%q/%q, want %q", tc.args, exitCode, stdout, stderr, tc.want)
			}
		}
	})

	t.Run("logs empty and error branches", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-empty-branches
agents:
  reviewer:
    provider: codex
`)
		listCalls := 0
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
					listCalls++
					if req.Msg.GetAgentName() == "reviewer" {
						return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("list unavailable"))
					}
					return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run missing"))
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath)
		if exitCode != 0 || stdout != "" || stderr != "" {
			t.Fatalf("logs empty text code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--json")
		if exitCode != 0 || stderr != "" || !strings.Contains(stdout, `"runs": null`) {
			t.Fatalf("logs empty json code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--agent", "reviewer")
		if exitCode != exitCodeUnavailable || stdout != "" || !strings.Contains(stderr, "list unavailable") {
			t.Fatalf("logs list error code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run", "missing")
		if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "run missing") {
			t.Fatalf("logs get error code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		if listCalls < 3 {
			t.Fatalf("listRuns calls = %d", listCalls)
		}
	})

	t.Run("inspect usage and service errors", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-inspect-branches
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			project: projectServiceStub{
				getProject: func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project missing"))
				},
				getSchedulerRun: func(context.Context, *connect.Request[agentcomposev2.GetSchedulerRunRequest]) (*connect.Response[agentcomposev2.GetSchedulerRunResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run missing"))
				},
			},
			run: runServiceStub{
				getRun: func(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("run missing"))
				},
			},
			session: sessionServiceStub{
				getSession: func(context.Context, *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("sandbox unavailable"))
				},
			},
		})
		defer server.Close()
		for _, tc := range []struct {
			args []string
			code int
			want string
		}{
			{args: []string{"inspect", "image"}, code: exitCodeUsage, want: "requires an image reference"},
			{args: []string{"inspect", "--file", composePath, "agent"}, code: exitCodeUsage, want: "requires an agent name"},
			{args: []string{"inspect", "--file", composePath, "run"}, code: exitCodeUsage, want: "requires a run id"},
			{args: []string{"inspect", "--file", composePath, "sandbox"}, code: exitCodeUsage, want: "requires a sandbox"},
			{args: []string{"inspect", "--file", composePath, "session"}, code: exitCodeUsage, want: "requires a sandbox"},
			{args: []string{"inspect", "--file", composePath, "unknown"}, code: exitCodeUsage, want: "unsupported inspect target"},
			{args: []string{"inspect", "--host", server.URL, "--file", composePath, "project"}, code: exitCodeUsage, want: "has not been started"},
			{args: []string{"inspect", "--host", server.URL, "--file", composePath, "run", "missing"}, code: exitCodeUsage, want: "run missing"},
			{args: []string{"inspect", "--host", server.URL, "--file", composePath, "sandbox", "sandbox-1"}, code: exitCodeUnavailable, want: "sandbox unavailable"},
		} {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != tc.code || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("%v code/stdout/stderr = %d/%q/%q, want code %d contains %q", tc.args, exitCode, stdout, stderr, tc.code, tc.want)
			}
		}
	})

	t.Run("image and cache removal text branches", func(t *testing.T) {
		server := newComposeServiceStubServer(t, composeServiceStubs{
			image: imageServiceStub{
				removeImage: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
					return connect.NewResponse(&agentcomposev2.RemoveImageResponse{ImageRef: req.Msg.GetImageRef()}), nil
				},
			},
			cache: cacheServiceStub{
				removeCache: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveCacheRequest]) (*connect.Response[agentcomposev2.RemoveCacheResponse], error) {
					return connect.NewResponse(&agentcomposev2.RemoveCacheResponse{
						Skipped:  []*agentcomposev2.CacheItem{{CacheId: req.Msg.GetCacheId(), BlockedReasons: []string{"remove failed"}}},
						Warnings: []string{"cache-force remove failed: permission denied"},
					}), nil
				},
			},
		})
		defer server.Close()
		stdout, stderr, _, exitCode := executeCLICommand("rmi", "--host", server.URL, "agent:unused")
		if exitCode != 0 || stderr != "" || stdout != "Removed: agent:unused\n" {
			t.Fatalf("rmi removed text code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
		stdout, stderr, _, exitCode = executeCLICommand("cache", "rm", "--host", server.URL, "--force", "cache-force")
		if exitCode != exitCodeUsage || stdout == "" || !strings.Contains(stderr, "permission denied") {
			t.Fatalf("cache rm force skipped code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
	})
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("write failed")
}
