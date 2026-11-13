package e2e

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"golang.org/x/net/http2"
)

// The real daemon, Docker driver, guest runner and Codex SDK execute. A local
// provider CLI fixture replaces the external model only and records invocations.
func TestE2EDockerRunInput(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerWorkspaceE2EImageEnv))
	if image == "" {
		t.Skip("set the existing Docker workspace image selector")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	repo := e2eRepoRoot(t)
	root, err := os.MkdirTemp("/tmp", "ac-input-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	binary := e2eDaemonBinary(t, ctx, repo, root)
	dockerClient := newE2EDockerClient(t, ctx, image)
	address := unusedLoopbackAddress(t)
	daemon := startE2EDaemon(t, binary, repo, root, address, image)
	base := "http://" + address
	waitForE2EDaemon(t, ctx, daemon, base)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(daemon.logs.String())
		}
	})
	unary := newE2EHTTPClient()
	defer unary.CloseIdleConnections()
	projects := agentcomposev2connect.NewProjectServiceClient(unary, base)
	runUnary := agentcomposev2connect.NewRunServiceClient(unary, base)
	sandboxes := agentcomposev2connect.NewSandboxServiceClient(unary, base)
	execs := newE2EExecClient(unary, base)
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	writeE2EHostFile(t, filepath.Join(source, "sales.txt"), "application-original")
	applied, err := projects.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Source: &agentcomposev2.ProjectSource{ProjectDir: source, ComposePath: filepath.Join(source, "agent-compose.yml")}, Spec: &agentcomposev2.ProjectSpec{Name: "run-input-e2e", Agents: []*agentcomposev2.AgentSpec{{Name: "worker", Provider: "codex", Image: image, Driver: &agentcomposev2.DriverSpec{Name: "docker", Config: &agentcomposev2.DriverSpec_Docker{Docker: &agentcomposev2.DockerDriverSpec{}}}, Workspace: &agentcomposev2.WorkspaceSpec{Provider: "file", Path: "."}, Env: []*agentcomposev2.EnvVarSpec{{Name: "CODEX_BIN", Value: "/data/state/codex-fixture"}}, Sandbox: &agentcomposev2.SandboxSpec{StoppedRuntimePolicy: "retain"}}}}}))
	if err != nil || !applied.Msg.GetApplied() {
		t.Fatalf("apply project=%v %v", applied, err)
	}
	projectID := applied.Msg.GetProject().GetSummary().GetProjectId()
	first := runE2EWorkspaceSandbox(t, ctx, runUnary, sandboxes, projectID, "bootstrap-first")
	second := runE2EWorkspaceSandbox(t, ctx, runUnary, sandboxes, projectID, "bootstrap-second")
	firstID, secondID := first.GetSandboxId(), second.GetSandboxId()
	t.Cleanup(func() {
		cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxes, firstID, false)
		cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxes, secondID, false)
	})
	installDockerSkillsE2ECodexFixture(t, ctx, execs, firstID)
	bidiHTTP := &http.Client{Transport: &http2.Transport{AllowHTTP: true, DialTLSContext: func(ctx context.Context, network, address string, _ *tls.Config) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}}
	defer bidiHTTP.CloseIdleConnections()
	client := agentcomposev2connect.NewRunServiceClient(bidiHTTP, base)
	declarations := []*agentcomposev2.RunInputFile{e2eRunInputFile("first", "sales.txt", []byte("attachment-one")), e2eRunInputFile("empty", "empty.txt", nil)}
	ready, runID := e2eAttachFileRound(t, ctx, client, projectID, firstID, "round-one", declarations, [][]byte{[]byte("attachment-one"), nil})
	if ready.GetFiles()[0].GetRelativePath() != "sales (1).txt" {
		t.Fatalf("rename mapping=%v", ready)
	}
	assertE2EWorkspaceFileContent(t, ctx, execs, firstID, "sales.txt", "application-original")
	assertE2EWorkspaceFileContent(t, ctx, execs, firstID, "sales (1).txt", "attachment-one")
	assertE2EWorkspaceFileAbsent(t, ctx, execs, secondID, "sales (1).txt")
	// Replay returns original evidence without a second provider invocation.
	replay, _ := e2eAttachFileRound(t, ctx, client, projectID, firstID, "round-one", declarations, nil)
	if replay.GetRunId() != runID || replay.GetFiles()[0].GetRelativePath() != "sales (1).txt" {
		t.Fatalf("replay=%v", replay)
	}
	assertDockerSkillsE2ECodexCalls(t, ctx, execs, firstID, 1)
	writeE2EWorkspaceFile(t, ctx, execs, firstID, "sales (1).txt", "agent-modified")
	removeE2EWorkspaceFile(t, ctx, execs, firstID, "empty.txt")
	e2eAttachFileRound(t, ctx, client, projectID, firstID, "round-two", []*agentcomposev2.RunInputFile{e2eRunInputFile("second", "sales.txt", []byte("attachment-two"))}, [][]byte{[]byte("attachment-two")})
	assertE2EWorkspaceFileContent(t, ctx, execs, firstID, "sales (1).txt", "agent-modified")
	assertE2EWorkspaceFileAbsent(t, ctx, execs, firstID, "empty.txt")
	assertE2EWorkspaceFileContent(t, ctx, execs, firstID, "sales (2).txt", "attachment-two")
	original, err := os.ReadFile(filepath.Join(source, "sales.txt"))
	if err != nil || string(original) != "application-original" {
		t.Fatal("application source changed")
	}
	// An incomplete, detached upload fails before the provider is invoked.
	failed := client.AttachAgentRun(ctx)
	if err := failed.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{Mode: agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT, DisconnectPolicy: agentcomposev2.AttachDisconnectPolicy_ATTACH_DISCONNECT_POLICY_DETACH, Request: &agentcomposev2.RunAgentRequest{ProjectId: projectID, AgentName: "worker", SandboxId: firstID, Prompt: "must not execute", CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING, ClientRequestId: "incomplete", InputFiles: []*agentcomposev2.RunInputFile{e2eRunInputFile("missing", "missing.txt", []byte("never"))}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := failed.CloseRequest(); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := failed.Receive()
		if err != nil {
			break
		}
		if frame.GetError() != nil {
			if frame.GetError().GetInputPreparation().GetState() != agentcomposev2.RunInputState_RUN_INPUT_STATE_FAILED {
				t.Fatalf("failure=%v", frame)
			}
			break
		}
	}
	if err := failed.CloseResponse(); err != nil {
		t.Fatal(err)
	}
	proof, err := runUnary.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{ProjectId: projectID, AgentName: "worker", Source: agentcomposev2.RunSource_RUN_SOURCE_MANUAL, ClientRequestId: "incomplete"}))
	if err != nil || proof.Msg.GetRun().GetInputPreparation().GetState() != agentcomposev2.RunInputState_RUN_INPUT_STATE_FAILED {
		t.Fatalf("durable failure=%v %v", proof, err)
	}
	assertDockerSkillsE2ECodexCalls(t, ctx, execs, firstID, 2)
	assertE2EWorkspaceFileAbsent(t, ctx, execs, firstID, "missing.txt")
	// Restart recovers the same delivery evidence without re-uploading or executing.
	daemon.stop(t)
	unary.CloseIdleConnections()
	bidiHTTP.CloseIdleConnections()
	daemon = startE2EDaemon(t, binary, repo, root, address, image)
	waitForE2EDaemon(t, ctx, daemon, base)
	recovered, err := runUnary.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{ProjectId: projectID, AgentName: "worker", Source: agentcomposev2.RunSource_RUN_SOURCE_MANUAL, ClientRequestId: "round-one"}))
	if err != nil || recovered.Msg.GetRun().GetInputPreparation().GetRunId() != runID || recovered.Msg.GetRun().GetInputPreparation().GetFiles()[0].GetRelativePath() != "sales (1).txt" {
		t.Fatalf("restart evidence=%v %v", recovered, err)
	}
	e2eAttachFileRound(t, ctx, client, projectID, firstID, "round-one", declarations, nil)
	if _, err := sandboxes.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: firstID})); err != nil {
		t.Fatal(err)
	}
	assertDockerSkillsE2ECodexCalls(t, ctx, execs, firstID, 2)
	assertE2EWorkspaceFileContent(t, ctx, execs, firstID, "sales (1).txt", "agent-modified")
	assertE2EWorkspaceFileAbsent(t, ctx, execs, firstID, "empty.txt")
	t.Log("real Docker: delivery, rename, zero bytes, two Runs sharing Sandbox, mutation/deletion preservation, replay, daemon restart and detached incomplete input verified; model provider uses local CLI fixture")
}

func e2eRunInputFile(id, name string, data []byte) *agentcomposev2.RunInputFile {
	sum := sha256.Sum256(data)
	return &agentcomposev2.RunInputFile{FileId: id, DisplayName: name, SizeBytes: uint64(len(data)), Sha256: sum[:]}
}

func e2eAttachFileRound(t *testing.T, ctx context.Context, client agentcomposev2connect.RunServiceClient, project, sandbox, request string, files []*agentcomposev2.RunInputFile, contents [][]byte) (*agentcomposev2.RunInputPreparation, string) {
	t.Helper()
	stream := client.AttachAgentRun(ctx)
	defer func() { _ = stream.CloseRequest(); _ = stream.CloseResponse() }()
	if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{Mode: agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT, Request: &agentcomposev2.RunAgentRequest{ProjectId: project, AgentName: "worker", SandboxId: sandbox, Prompt: "Complete the local file input verification fixture.", ClientRequestId: request, Source: agentcomposev2.RunSource_RUN_SOURCE_MANUAL, CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING, InputFiles: files}}}}); err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		if contents == nil {
			sent <- nil
			return
		}
		for i, data := range contents {
			if len(data) > 0 {
				if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_FileChunk{FileChunk: &agentcomposev2.RunInputFileChunk{FileId: files[i].GetFileId(), Data: data}}}); err != nil {
					sent <- err
					return
				}
			}
		}
		sent <- stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_InputComplete{InputComplete: &agentcomposev2.RunInputComplete{}}})
	}()
	var ready *agentcomposev2.RunInputPreparation
	for {
		frame, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) && contents == nil && ready != nil {
				break
			}
			t.Fatalf("attach %s: %v", request, err)
		}
		if frame.GetError() != nil {
			encoded, _ := json.Marshal(frame.GetError())
			t.Fatalf("attach %s: %s", request, encoded)
		}
		if frame.GetInputReady() != nil {
			ready = frame.GetInputReady()
			if contents == nil {
				break
			}
		}
		if frame.GetAgentTurnCompleted() != nil {
			if err := stream.Send(&agentcomposev2.AttachAgentRunRequest{Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}}); err != nil {
				t.Fatal(err)
			}
		}
		if result := frame.GetResult(); result != nil {
			if !result.GetSuccess() {
				t.Fatal(result)
			}
			break
		}
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if ready == nil || ready.GetState() != agentcomposev2.RunInputState_RUN_INPUT_STATE_READY || ready.GetSandboxId() != sandbox {
		t.Fatal(fmt.Sprintf("missing ready evidence: %v", ready))
	}
	return ready, ready.GetRunId()
}
