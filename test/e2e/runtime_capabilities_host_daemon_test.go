package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// Opt-in uses a disposable loopback OB and an image containing the candidate SDK
// plus testdata/capability-program.mjs at /opt/capability-program/program.mjs.
func TestE2EDockerRuntimeCapabilities(t *testing.T) {
	image, obAddr := os.Getenv("AC_CAPABILITIES_E2E_IMAGE"), os.Getenv("AC_LOCAL_OB_ADDR")
	if image == "" || obAddr == "" {
		t.Skip("requires AC_CAPABILITIES_E2E_IMAGE and isolated AC_LOCAL_OB_ADDR")
	}
	host, _, err := net.SplitHostPort(obAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		t.Fatal("OB must use a literal loopback address")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	repo := e2eRepoRoot(t)
	if err := os.MkdirAll(filepath.Join(repo, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(repo, ".cache"), "capabilities-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	// macOS limits Unix socket paths to 104 bytes; keep only the socket short.
	socketRoot, err := os.MkdirTemp("/tmp", "ac-cap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := e2eDaemonBinary(t, ctx, repo, root)
	dockerClient := newE2EDockerClient(t, ctx, image)
	address := unusedLoopbackAddress(t)
	baseURL := "http://" + address
	capAddress := unusedLoopbackAddress(t)
	_, capPort, _ := net.SplitHostPort(capAddress)
	env := map[string]string{
		"AGENT_COMPOSE_SOCKET":           filepath.Join(socketRoot, "daemon.sock"),
		"CAP_GRPC_LISTEN":                "0.0.0.0:" + capPort,
		"CAP_GRPC_TARGET":                "host.docker.internal:" + capPort,
		"AGENT_COMPOSE_RUNTIME_BASE_URL": dockerDesktopRuntimeBaseURL(t, address),
	}
	listen := strings.Replace(address, "127.0.0.1:", "0.0.0.0:", 1)
	daemon := startE2EDaemonWithEnv(t, binary, repo, dataRoot, listen, image, env)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("capability daemon log:\n%s", daemon.logs.String())
		}
	})
	projectID := applyCapabilityE2EProject(t, ctx, binary, baseURL, root, image, obAddr)
	httpClient := newE2EHTTPClient()
	httpClient.Timeout = 3 * time.Minute
	defer httpClient.CloseIdleConnections()
	runs := agentcomposev2connect.NewRunServiceClient(httpClient, baseURL)
	sandboxes := agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL)
	execs := newE2EExecClient(httpClient, baseURL)
	bootstrap, err := runs.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId: projectID, AgentName: "worker", Command: "true",
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "capability-bootstrap",
	}))
	if err != nil {
		t.Fatal(err)
	}
	id := bootstrap.Msg.GetRun().GetSummary().GetSandboxId()
	removed := false
	t.Cleanup(func() { cleanupE2EWorkspaceSandbox(t, dockerClient, sandboxes, id, removed) })
	if bootstrap.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("bootstrap failed: %s", bootstrap.Msg.GetRun().GetSummary().GetError())
	}
	input := map[string]any{"instance": "ip-evidence-test", "method": "ac.evidence.v1.EvidenceService/SearchEvents", "field": "ip", "params": map[string]string{"ip": "192.0.2.44"}}
	request, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	command := runE2EWorkspaceCommand(t, ctx, execs, id, "node", "/opt/capability-program/program.mjs", string(request))
	requireE2EExecSuccess(t, "plain program calls discovered OB tool", command)
	assertCapabilityE2EResult(t, command.GetStdout())
	assertCapabilityE2EDescriptor(t, command.GetStdout())
	prompt, err := runs.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId: projectID, AgentName: "worker", SandboxId: id, Prompt: string(request),
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "generic-capability-prompt",
	}))
	if err != nil {
		t.Fatal(err)
	}
	run := prompt.Msg.GetRun()
	if run.GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("prompt run failed: %s", run.GetSummary().GetError())
	}
	assertCapabilityE2EResult(t, run.GetOutput()+run.GetResultJson())
	denial := runE2EWorkspaceCommand(t, ctx, execs, id, "node", "/opt/capability-program/program.mjs", `{"deniedCapset":"other/dev","instance":"ip-evidence-test","deniedMethod":"ac.evidence.v1.EvidenceService/SearchEvents"}`)
	requireE2EExecSuccess(t, "raw gRPC forbidden capset denied", denial)
	if !strings.Contains(denial.GetStdout(), `\"denied\":true`) {
		t.Fatal("raw authorization denial not reported")
	}
	if _, err := sandboxes.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: id})); err != nil {
		t.Fatal(err)
	}
	// Persisted project routing and discovery must survive a normal daemon restart.
	daemon.stop(t)
	daemon = startE2EDaemonWithEnv(t, binary, repo, dataRoot, listen, image, env)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	if _, err := sandboxes.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: id})); err != nil {
		t.Fatal(err)
	}
	resumed := runE2EWorkspaceCommand(t, ctx, execs, id, "node", "/opt/capability-program/program.mjs", string(request))
	requireE2EExecSuccess(t, "discovery and call after daemon restart/resume", resumed)
	assertCapabilityE2EResult(t, resumed.GetStdout())
	assertCapabilityE2EDescriptor(t, resumed.GetStdout())
	credential := runE2EWorkspaceCommand(t, ctx, execs, id, "node", "-e", `process.stdout.write(JSON.stringify({token:process.env.CAP_TOKEN || ""}))`)
	requireE2EExecSuccess(t, "capture test-owned CAP credential without logging", credential)
	var captured struct{ Token string }
	if json.Unmarshal([]byte(credential.GetStdout()), &captured) != nil || captured.Token == "" {
		t.Fatal("sandbox CAP credential missing")
	}
	connection, err := grpc.NewClient(capAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	credentialCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("x-capability-sandbox-token", captured.Token, "x-octobus-capset", "tools/dev", "x-octobus-instance", "ip-evidence-test"))
	activeCtx, stopActive := context.WithTimeout(credentialCtx, 5*time.Second)
	// SearchRequest has one string at field 1; use its standard wire-equivalent
	// message to confirm the captured credential is valid before removal.
	err = connection.Invoke(activeCtx, "/ac.evidence.v1.EvidenceService/SearchEvents", wrapperspb.String("192.0.2.44"), &emptypb.Empty{})
	stopActive()
	if err != nil {
		t.Fatalf("captured credential was not valid before removal: %s", status.Code(err))
	}
	removeE2ESandboxPublic(t, ctx, sandboxes, id)
	removed = true
	assertE2EDockerSandboxContainerCount(t, ctx, dockerClient, id, 0)
	// Removal may take longer than an RPC deadline; give each assertion its own
	// budget so a slow container shutdown cannot masquerade as an auth failure.
	revokedCtx, stopRevoked := context.WithTimeout(credentialCtx, 5*time.Second)
	defer stopRevoked()
	err = connection.Invoke(revokedCtx, "/ac.evidence.v1.EvidenceService/SearchEvents", wrapperspb.String("192.0.2.44"), &emptypb.Empty{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("removed sandbox's credential status=%s, want Unauthenticated", status.Code(err))
	}
	projects := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	if _, err := projects.RemoveProject(ctx, connect.NewRequest(&agentcomposev2.RemoveProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_ProjectId{ProjectId: projectID}}})); err != nil {
		t.Fatal(err)
	}
	t.Log("plain program and Codex-compatible prompt use the same SDK; real OB calls, raw authorization denial, restart/resume, public removal and old CAP rejection verified")
}

func assertCapabilityE2EDescriptor(t *testing.T, output string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		var event struct{ Item struct{ Text string } }
		if json.Unmarshal([]byte(line), &event) != nil || event.Item.Text == "" {
			continue
		}
		var answer struct{ Descriptor string }
		if err := json.Unmarshal([]byte(event.Item.Text), &answer); err != nil {
			t.Fatal(err)
		}
		raw, err := base64.StdEncoding.DecodeString(answer.Descriptor)
		if err != nil {
			t.Fatal(err)
		}
		var set descriptorpb.FileDescriptorSet
		if err := proto.Unmarshal(raw, &set); err != nil {
			t.Fatal(err)
		}
		files, err := protodesc.NewFiles(&set)
		if err != nil {
			t.Fatalf("descriptor dependencies missing: %v", err)
		}
		if _, err := files.FindDescriptorByName("ac.evidence.v1.EventSummary"); err != nil || len(set.File) < 2 {
			t.Fatal("imported message definition absent from descriptor")
		}
		return
	}
	t.Fatal("no structured program answer")
}

func applyCapabilityE2EProject(t *testing.T, ctx context.Context, binary, baseURL, root, image, obAddr string) string {
	t.Helper()
	yaml := fmt.Sprintf(`name: runtime-capabilities-e2e
octobus_servers:
  tools:
    url: http://%s
    token: ${AC_LOCAL_OB_ADMIN_TOKEN}
agents:
  worker:
    provider: codex
    image: %s
    driver:
      docker: {}
    env:
      CODEX_BIN: /opt/capability-program/program.mjs
    capset_ids: [tools/dev]
    sandbox:
      stopped_runtime_policy: retain
`, obAddr, image)
	file := filepath.Join(root, "agent-compose.yml")
	writeE2EHostFile(t, file, yaml)
	cmd := exec.CommandContext(ctx, binary, "up", "--host", baseURL, "--file", file)
	cmd.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_AUTH_TOKEN": "", "AUTH_PASSWORD": "", "AUTH_USERNAME": ""})
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply capability project: %v\n%s", err, output)
	}
	client := newE2EHTTPClient()
	defer client.CloseIdleConnections()
	projects := agentcomposev2connect.NewProjectServiceClient(client, baseURL)
	response, err := projects.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_Name{Name: "runtime-capabilities-e2e"}}}))
	if err != nil {
		t.Fatal(err)
	}
	return response.Msg.GetProject().GetSummary().GetProjectId()
}

func assertCapabilityE2EResult(t *testing.T, output string) {
	t.Helper()
	// Prompt output may wrap the same NDJSON answer as escaped result JSON.
	output = strings.ReplaceAll(output, `\"`, `"`)
	if !strings.Contains(output, `"total":2`) || !strings.Contains(output, `"capset":"tools/dev"`) || !strings.Contains(output, "1e6a86f8fee1759165c5") {
		t.Fatalf("generic program did not return the discovered tool's real records: %s", output)
	}
}
