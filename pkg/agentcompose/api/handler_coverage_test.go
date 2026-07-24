package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestTranscriptEventFromExecChunkMapsStdioStream(t *testing.T) {
	createdAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		stream domain.StdioStream
		want   agentcomposev2.StdioStream
	}{
		{name: "stdout", stream: domain.StdioStdout, want: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT},
		{name: "stderr", stream: domain.StdioStderr, want: agentcomposev2.StdioStream_STDIO_STREAM_STDERR},
		{name: "unspecified", want: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT},
		{name: "unknown", stream: domain.StdioStream("other"), want: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := TranscriptEventFromExecChunk(domain.ExecChunk{Text: "chunk", Stream: tc.stream}, createdAt)
			if event.GetStream() != tc.want || event.GetText() != "chunk" || event.GetCreatedAt() == nil {
				t.Fatalf("event = %#v, want stream %s", event, tc.want)
			}
		})
	}
}

func TestPrepareStreamingHeadersPreservesNoTransform(t *testing.T) {
	headers := http.Header{}
	PrepareStreamingHeaders(headers)
	if got, want := headers.Get("Cache-Control"), "no-cache, no-transform"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if got, want := headers.Get("X-Accel-Buffering"), "no"; got != want {
		t.Fatalf("X-Accel-Buffering = %q, want %q", got, want)
	}
}

func TestIntegrationAPIHandlerRuntimeWorkflows(t *testing.T) {
	t.Run("streaming headers", TestPrepareStreamingHeadersPreservesNoTransform)
	t.Run("sandbox remove race", TestRemoveSandboxRemoveRaceRemainsInternal)
	t.Run("sandbox remove lifecycle", TestV2SandboxRemoveUsesSandboxIDAndSessionCompatibilityDelegate)
	t.Run("sandbox remove running guard", TestV2SandboxRemoveRejectsRunningWithoutForce)
	t.Run("sandbox remove preserves metadata on runtime failure", TestV2SandboxRemoveKeepsMetadataWhenRuntimeRemovalFails)
	t.Run("sandbox remove validation and storage failures", TestV2SandboxRemoveValidationAndStoreErrors)
	t.Run("sandbox stats runtime metrics", TestGetSandboxStatsReturnsRuntimeMetrics)
	t.Run("sandbox stats stopped sandbox", TestGetSandboxStatsRejectsStoppedSandbox)
	t.Run("sandbox stats unsupported runtime", TestGetSandboxStatsUnsupportedRuntimeIsUnimplemented)
	t.Run("sandbox legacy history", TestV2ListSandboxHistoryReturnsLegacyCellsAndEvents)
	t.Run("sandbox watch projection", TestV2SandboxWatchEventProjection)
	t.Run("domain connect error", TestConnectErrorForDomainClassifiesReusableSentinels)
	t.Run("compiled runtime connect boundaries", TestRuntimeDriverNotCompiledConnectBoundaries)
	t.Run("image pull inspect and skip", TestImagePullInspectAndSkip)
	t.Run("exec sandbox target", TestExecHandlerSandboxTargetWorkflow)
	t.Run("exec run selector and stream sender", TestExecHandlerRunSelectorAndStreamSenderWorkflow)
	t.Run("exec selector errors", TestExecHandlerSelectorErrors)
	t.Run("project and run store backed handlers", TestProjectAndRunHandlersStoreBackedWorkflows)
	t.Run("follow run logs offsets tail and final", TestFollowRunLogsStreamsOffsetsTailAndFinal)
	t.Run("follow run logs missing terminal file", TestFollowRunLogsMissingLogFileReturnsEmptyFinalForTerminalRun)
	t.Run("follow run logs project mismatch", TestFollowRunLogsRejectsProjectMismatch)
}

func TestE2EAPIHandlerRuntimeWorkflows(t *testing.T) {
	TestIntegrationAPIHandlerRuntimeWorkflows(t)
}

func TestRemoveSandboxRemoveRaceRemainsInternal(t *testing.T) {
	sandboxID := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	store := &apiSandboxStore{
		session:   &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}},
		removeErr: os.ErrNotExist,
	}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, &characterizationSandboxRemover{}, nil)
	_, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: sandboxID}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("RemoveSandbox error code = %v, want %v; err=%v", connect.CodeOf(err), connect.CodeInternal, err)
	}
}

func TestGetSandboxStatsReturnsRuntimeMetrics(t *testing.T) {
	sandboxID := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	value := 42.5
	store := &apiSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, Driver: "docker", VMStatus: domain.VMStatusRunning}},
		vmState: domain.VMState{
			Driver: "docker",
			BoxID:  "box-1",
		},
	}
	runtime := &apiStatsRuntime{stats: domain.SandboxStats{
		SandboxID:        sandboxID,
		Driver:           "docker",
		SampledAt:        time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC),
		CPUPercent:       domain.MetricValue{Value: &value, Unit: "percent", Status: domain.MetricStatusOK},
		MemoryUsageBytes: domain.MetricValue{Unit: "bytes", Status: domain.MetricStatusUnknown},
	}}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, nil, nil, func(*domain.Sandbox) (SandboxStatsRuntime, error) {
		return runtime, nil
	})
	resp, err := handler.GetSandboxStats(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: sandboxID}))
	if err != nil {
		t.Fatalf("GetSandboxStats returned error: %v", err)
	}
	if resp.Msg.GetStats().GetCpuPercent().GetStatus() != agentcomposev2.MetricStatus_METRIC_STATUS_OK || resp.Msg.GetStats().GetCpuPercent().GetValue() != value {
		t.Fatalf("GetSandboxStats response = %#v", resp.Msg.GetStats())
	}
	if resp.Msg.GetStats().GetMemoryUsageBytes().GetStatus() != agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN {
		t.Fatalf("memory metric = %#v", resp.Msg.GetStats().GetMemoryUsageBytes())
	}
	if runtime.sessionID != sandboxID || runtime.vmState.BoxID != "box-1" {
		t.Fatalf("runtime call session/vm = %q/%#v", runtime.sessionID, runtime.vmState)
	}
}

func TestGetSandboxStatsRejectsStoppedSandbox(t *testing.T) {
	sandboxID := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	store := &apiSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusStopped}},
	}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, nil, nil, func(*domain.Sandbox) (SandboxStatsRuntime, error) {
		return &apiStatsRuntime{}, nil
	})
	_, err := handler.GetSandboxStats(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: sandboxID}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("GetSandboxStats stopped code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestGetSandboxStatsUnsupportedRuntimeIsUnimplemented(t *testing.T) {
	sandboxID := "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	store := &apiSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, VMStatus: domain.VMStatusRunning}},
	}
	handler := NewSandboxHandler(&characterizationSessionDelegate{}, store, nil, nil)
	_, err := handler.GetSandboxStats(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: sandboxID}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("GetSandboxStats unsupported code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestConnectErrorForDomainClassifiesReusableSentinels(t *testing.T) {
	tests := []struct {
		err  error
		code connect.Code
	}{
		{err: domain.ClassifyError(domain.ErrUnsupported, "stats are unsupported", nil), code: connect.CodeUnimplemented},
		{err: domain.ResourceError(domain.ErrNotFound, "sandbox", "missing", "", nil), code: connect.CodeNotFound},
		{err: domain.ClassifyError(domain.ErrInvalidArgument, "bad request", nil), code: connect.CodeInvalidArgument},
		{err: domain.ClassifyError(domain.ErrRequired, "project is required", nil), code: connect.CodeInvalidArgument},
		{err: domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox stopped", nil), code: connect.CodeFailedPrecondition},
		{err: domain.ClassifyError(domain.ErrAlreadyExists, "project exists", nil), code: connect.CodeAlreadyExists},
		{err: context.Canceled, code: connect.CodeCanceled},
		{err: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded},
		{err: errors.New("boom"), code: connect.CodeInternal},
	}
	for _, tc := range tests {
		if got := connect.CodeOf(ConnectErrorForDomain(tc.err)); got != tc.code {
			t.Fatalf("ConnectErrorForDomain(%v) = %v, want %v", tc.err, got, tc.code)
		}
	}
}

func TestImagePullInspectAndSkip(t *testing.T) {
	ctx := context.Background()
	local := testImageBackend{
		inspect: images.InspectResult{Image: &agentcomposev2.Image{
			ImageRef:    "guest:latest",
			ResolvedRef: "guest@sha256:local",
		}},
	}
	handler := NewImageHandler(fakeImageSelector{backend: &local})
	resp, err := handler.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "guest:latest"}))
	if err != nil {
		t.Fatalf("PullImage local returned error: %v", err)
	}
	if local.pullCalls != 0 {
		t.Fatalf("PullImage local pull calls = %d, want 0", local.pullCalls)
	}
	if resp.Msg.GetResolvedRef() != "guest@sha256:local" || len(resp.Msg.GetWarnings()) == 0 {
		t.Fatalf("PullImage local response = %#v", resp.Msg)
	}

	missing := testImageBackend{
		inspectErr: images.OpError{Op: "inspect image", ImageRef: "missing:latest", Err: imagecache.NewError(imagecache.ErrorKindNotFound, "inspect", "missing:latest", errors.New("missing"))},
		pull: images.PullResult{Image: &agentcomposev2.Image{
			ImageRef:    "missing:latest",
			ResolvedRef: "missing@sha256:pulled",
		}, ResolvedRef: "missing@sha256:pulled"},
	}
	handler = NewImageHandler(fakeImageSelector{backend: &missing})
	resp, err = handler.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "missing:latest"}))
	if err != nil {
		t.Fatalf("PullImage missing returned error: %v", err)
	}
	if missing.pullCalls != 1 {
		t.Fatalf("PullImage missing pull calls = %d, want 1", missing.pullCalls)
	}
	if resp.Msg.GetResolvedRef() != "missing@sha256:pulled" || len(resp.Msg.GetWarnings()) != 0 {
		t.Fatalf("PullImage missing response = %#v", resp.Msg)
	}
}

type fakeImageSelector struct{ backend images.Backend }

func (s fakeImageSelector) ImageBackendForStore(agentcomposev2.ImageStoreKind) (images.Backend, error) {
	return s.backend, nil
}

func TestImageBuildStreamsEvents(t *testing.T) {
	backend := &testImageBackend{}
	handler := NewImageHandler(fakeImageSelector{backend: backend})
	client, closeServer := newImageHandlerTestClient(t, handler)
	defer closeServer()

	stream, err := client.BuildImage(context.Background(), connect.NewRequest(&agentcomposev2.BuildImageRequest{
		ContextDir: "/tmp/context",
		Dockerfile: "Dockerfile.agent",
		Tags:       []string{"agent:dev", "agent:dev"},
		BuildArgs:  map[string]string{"NODE_ENV": "development"},
		Target:     "runtime",
		Platform:   &agentcomposev2.ImagePlatform{Os: "linux", Architecture: "amd64"},
		NoCache:    true,
		Pull:       true,
	}))
	if err != nil {
		t.Fatalf("BuildImage setup error: %v", err)
	}
	var events []*agentcomposev2.BuildImageEvent
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("BuildImage stream error: %v", err)
	}
	if len(events) != 1 || events[0].GetResolvedRef() != "agent:dev@sha256:built" {
		t.Fatalf("BuildImage events = %#v", events)
	}
	if backend.buildReq.ContextDir != "/tmp/context" || backend.buildReq.Dockerfile != "Dockerfile.agent" || backend.buildReq.Target != "runtime" || !backend.buildReq.NoCache || !backend.buildReq.Pull {
		t.Fatalf("BuildImage backend request = %#v", backend.buildReq)
	}
	if len(backend.buildReq.Tags) != 1 || backend.buildReq.Tags[0] != "agent:dev" {
		t.Fatalf("BuildImage tags = %#v", backend.buildReq.Tags)
	}
	if backend.buildReq.BuildArgs["NODE_ENV"] != "development" || backend.buildReq.Platform.GetArchitecture() != "amd64" {
		t.Fatalf("BuildImage args/platform = %#v / %#v", backend.buildReq.BuildArgs, backend.buildReq.Platform)
	}

	empty, err := client.BuildImage(context.Background(), connect.NewRequest(&agentcomposev2.BuildImageRequest{}))
	if err != nil {
		t.Fatalf("BuildImage empty setup error: %v", err)
	}
	for empty.Receive() {
	}
	if code := connect.CodeOf(empty.Err()); code != connect.CodeInvalidArgument {
		t.Fatalf("BuildImage empty code = %s, want invalid argument (err=%v)", code, empty.Err())
	}
}

type testImageBackend struct {
	inspect    images.InspectResult
	inspectErr error
	pull       images.PullResult
	pullErr    error
	pullCalls  int
	buildReq   images.BuildRequest
}

func (b *testImageBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (b *testImageBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	b.pullCalls++
	return b.pull, b.pullErr
}

func (b *testImageBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return b.inspect, b.inspectErr
}

func (b *testImageBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

func (b *testImageBackend) BuildImage(_ context.Context, req images.BuildRequest, sink images.BuildEventSink) (images.BuildResult, error) {
	b.buildReq = req
	image := &agentcomposev2.Image{ImageId: "sha256:built", ImageRef: req.Tags[0], ResolvedRef: req.Tags[0] + "@sha256:built"}
	if err := sink.Send(&agentcomposev2.BuildImageEvent{
		Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
		ImageRef:    req.Tags[0],
		ResolvedRef: req.Tags[0] + "@sha256:built",
		Image:       image,
	}); err != nil {
		return images.BuildResult{}, err
	}
	return images.BuildResult{Image: image, ImageRef: req.Tags[0], ResolvedRef: req.Tags[0] + "@sha256:built"}, nil
}

func newImageHandlerTestClient(t *testing.T, handler *ImageHandler) (agentcomposev2connect.ImageServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, serviceHandler := agentcomposev2connect.NewImageServiceHandler(handler)
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewImageServiceClient(server.Client(), server.URL), server.Close
}

func TestExecHandlerSandboxTargetWorkflow(t *testing.T) {
	ctx := context.Background()
	sandboxRoot := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sandboxRoot, "workspace")}}
	store := &apiExecSandboxStore{sandbox: sandbox, vm: domain.VMState{Driver: "docker"}}
	runtime := &apiExecRuntime{}
	handler := NewExecHandler(&appconfig.Config{}, store, apiExecProjectStore{}, func(*domain.Sandbox) (ExecRuntime, error) {
		return runtime, nil
	})
	resp, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-1"},
		Command: &agentcomposev2.ExecCommand{Command: "echo", Args: []string{"hi"}},
		Env:     []*agentcomposev2.EnvVarSpec{{Name: "FOO", Value: "bar"}, {Name: " "}},
	}))
	if err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
	if !resp.Msg.GetResult().GetSuccess() || resp.Msg.GetResult().GetStdout() != "hi\n" || runtime.spec.Command != "sh" || !strings.Contains(strings.Join(runtime.spec.Args, " "), "agent-compose-runtime exec") {
		t.Fatalf("exec resp=%#v spec=%#v", resp.Msg.GetResult(), runtime.spec)
	}
	requestFiles, err := filepath.Glob(filepath.Join(sandboxRoot, "state", "exec", "*", "command-request.json"))
	if err != nil || len(requestFiles) != 1 {
		t.Fatalf("command request files=%#v err=%v", requestFiles, err)
	}
	var commandRequest execution.RuntimeCommandRequest
	data, err := os.ReadFile(requestFiles[0])
	if err != nil || json.Unmarshal(data, &commandRequest) != nil {
		t.Fatalf("read command request data=%q err=%v", string(data), err)
	}
	if commandRequest.Env["FOO"] != "bar" || commandRequest.Command != "echo" || len(commandRequest.Args) != 1 || commandRequest.Args[0] != "hi" {
		t.Fatalf("command request = %#v", commandRequest)
	}
	outputData, err := os.ReadFile(filepath.Join(filepath.Dir(requestFiles[0]), "output.txt"))
	if err != nil || string(outputData) != "hi\n" {
		t.Fatalf("exec output artifact = %q err=%v", string(outputData), err)
	}
	if _, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-1"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected missing command error, got %v", err)
	}
	store.sandbox = &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusStopped}}
	if _, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-1"}, Command: &agentcomposev2.ExecCommand{Command: "echo"}})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected stopped sandbox error, got %v", err)
	}
}

func TestExecHandlerRunSelectorAndStreamSenderWorkflow(t *testing.T) {
	ctx := context.Background()
	sandboxRoot := t.TempDir()
	running := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-running", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sandboxRoot, "workspace")}}
	store := &apiExecWorkflowSandboxStore{
		sandboxes: map[string]*domain.Sandbox{
			"sandbox-running": running,
			"sandbox-second":  {Summary: domain.SandboxSummary{ID: "sandbox-second", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sandboxRoot, "workspace-2")}},
			"sandbox-stopped": {Summary: domain.SandboxSummary{ID: "sandbox-stopped", VMStatus: domain.VMStatusStopped}},
		},
		vm: domain.VMState{Driver: "docker"},
	}
	projects := &apiExecWorkflowProjectStore{
		projects: []domain.ProjectRecord{
			{ID: "project-1", Name: "Project", SourcePath: "/repo/one"},
			{ID: "project-2", Name: "Project", SourcePath: "/repo/two"},
		},
		runs: []domain.ProjectRunRecord{
			{RunID: "run-1", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", SandboxID: "sandbox-running"},
			{RunID: "run-second", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", SandboxID: "sandbox-second"},
			{RunID: "run-stopped", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", SandboxID: "sandbox-stopped"},
			{RunID: "run-nosandbox", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker"},
		},
	}
	runtime := &apiExecRuntime{}
	handler := NewExecHandler(&appconfig.Config{}, store, projects, func(*domain.Sandbox) (ExecRuntime, error) {
		return runtime, nil
	})

	var events []*agentcomposev2.ExecStreamResponse
	resp, err := handler.executeProjectCommand(ctx, &agentcomposev2.ExecRequest{
		Target:    &agentcomposev2.ExecRequest_RunId{RunId: "run-1"},
		Command:   &agentcomposev2.ExecCommand{Command: "echo", Args: []string{"hi"}},
		Cwd:       "/custom",
		Env:       []*agentcomposev2.EnvVarSpec{{Name: "A", Value: "B"}},
		TimeoutMs: 1000,
	}, "exec-run", func(event *agentcomposev2.ExecStreamResponse) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("executeProjectCommand run target returned error: %v", err)
	}
	if resp.GetRunId() != "run-1" || resp.GetSandboxId() != "sandbox-running" || resp.GetCwd() != "/custom" || len(events) < 2 {
		t.Fatalf("run target resp=%#v events=%#v", resp, events)
	}
	if events[0].GetEventType() != agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_STARTED ||
		events[1].GetEventType() != agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT ||
		events[1].GetTranscript().GetText() == "" {
		t.Fatalf("stream events = %#v", events)
	}
	if events[1].GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDOUT ||
		events[1].GetTranscript().GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDOUT {
		t.Fatalf("stream event stdio stream = event %s transcript %s", events[1].GetStream(), events[1].GetTranscript().GetStream())
	}
	for _, event := range events {
		if strings.Contains(event.GetChunk(), execution.CommandResultPrefix) || strings.Contains(event.GetTranscript().GetText(), execution.CommandResultPrefix) {
			t.Fatalf("stream event leaked command payload: %#v", event)
		}
	}
	transcript, err := os.ReadFile(filepath.Join(sandboxRoot, "state", "exec", "exec-run", "transcript.txt"))
	if err != nil || !strings.Contains(string(transcript), "hi\n") || strings.Contains(string(transcript), execution.CommandResultPrefix) {
		t.Fatalf("transcript = %q err=%v", string(transcript), err)
	}

	if _, err := handler.executeProjectCommand(ctx, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSandboxSelector{ProjectId: "project-1", AgentName: "worker"}},
		Command: &agentcomposev2.ExecCommand{Command: "echo"},
	}, "exec-ambiguous", nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected multiple running selector error, got %v", err)
	}
	projects.runs = projects.runs[:1]
	selectorResp, err := handler.executeProjectCommand(ctx, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSandboxSelector{ProjectId: "project-1", AgentName: "worker"}},
		Command: &agentcomposev2.ExecCommand{Command: "echo"},
	}, "exec-selector", nil)
	if err != nil {
		t.Fatalf("selector command returned error: %v", err)
	}
	if selectorResp.GetRunId() != "run-1" || selectorResp.GetSandboxId() != "sandbox-running" {
		t.Fatalf("selector resp = %#v", selectorResp)
	}
	projects.runs = nil
	if _, err := handler.executeProjectCommand(ctx, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSandboxSelector{ProjectId: "project-1", AgentName: "worker"}},
		Command: &agentcomposev2.ExecCommand{Command: "echo"},
	}, "exec-missing", nil); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected no running sandbox selector error, got %v", err)
	}
	projects.runs = []domain.ProjectRunRecord{
		{RunID: "run-stopped", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", SandboxID: "sandbox-stopped"},
		{RunID: "run-nosandbox", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker"},
	}
	if _, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_RunId{RunId: "run-nosandbox"},
		Command: &agentcomposev2.ExecCommand{Command: "echo"},
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected no sandbox run error, got %v", err)
	}
	if _, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_RunId{RunId: "run-stopped"},
		Command: &agentcomposev2.ExecCommand{Command: "echo"},
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected stopped run sandbox error, got %v", err)
	}
	if _, err := handler.resolveProjectRef(ctx, &agentcomposev2.ProjectRef{Name: "Project"}); !errors.Is(err, domain.ErrAmbiguous) {
		t.Fatalf("expected ambiguous project ref error, got %v", err)
	}
}

func TestExecHandlerSelectorErrors(t *testing.T) {
	handler := NewExecHandler(&appconfig.Config{}, &apiExecSandboxStore{}, apiExecProjectStore{err: sql.ErrNoRows}, func(*domain.Sandbox) (ExecRuntime, error) {
		return &apiExecRuntime{}, nil
	})
	if _, err := handler.Exec(context.Background(), connect.NewRequest(&agentcomposev2.ExecRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected target error, got %v", err)
	}
	if _, err := handler.Exec(context.Background(), connect.NewRequest(&agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_RunId{RunId: "missing"}, Command: &agentcomposev2.ExecCommand{Command: "echo"}})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected run not found, got %v", err)
	}
}

func TestExecAttachRequiresStartFrame(t *testing.T) {
	handler := newExecAttachTestHandler(t, &apiExecRuntime{})
	err := handler.execAttach(context.Background(), sliceExecAttachReceiver(), func(*agentcomposev2.ExecAttachResponse) error {
		t.Fatal("unexpected send")
		return nil
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "start frame") {
		t.Fatalf("empty stream error = %v", err)
	}

	err = handler.execAttach(context.Background(), sliceExecAttachReceiver(&agentcomposev2.ExecAttachRequest{
		Frame: &agentcomposev2.ExecAttachRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: []byte("x")}},
	}), func(*agentcomposev2.ExecAttachResponse) error {
		t.Fatal("unexpected send")
		return nil
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "first exec attach frame") {
		t.Fatalf("non-start first frame error = %v", err)
	}
}

func TestExecAttachRuntimeUnsupportedIsUnimplemented(t *testing.T) {
	handler := newExecAttachTestHandler(t, &apiExecRuntime{})
	var sent []*agentcomposev2.ExecAttachResponse
	err := handler.execAttach(context.Background(), sliceExecAttachReceiver(execAttachStartRequest()), func(resp *agentcomposev2.ExecAttachResponse) error {
		sent = append(sent, resp)
		return nil
	})
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported runtime error = %v", err)
	}
	if len(sent) != 0 {
		t.Fatalf("unexpected responses = %#v", sent)
	}
}

func TestExecAttachProjectsStdoutAndResult(t *testing.T) {
	runtime := &apiAttachRuntime{interaction: &apiAttachInteraction{outputs: []driverpkg.RuntimeOutputFrame{
		{Type: driverpkg.RuntimeOutputStarted},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte("out\n")},
		{Type: driverpkg.RuntimeOutputStderr, Data: []byte("err\n")},
		{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{ExitCode: 7, Success: false, Error: "boom"}},
	}}}
	handler := newExecAttachTestHandler(t, runtime)
	var sent []*agentcomposev2.ExecAttachResponse
	err := handler.execAttach(context.Background(), sliceExecAttachReceiver(execAttachStartRequest()), func(resp *agentcomposev2.ExecAttachResponse) error {
		sent = append(sent, resp)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecAttach returned error: %v", err)
	}
	if len(sent) != 4 {
		t.Fatalf("responses = %#v", sent)
	}
	if started := sent[0].GetStarted(); started.GetExecId() == "" || started.GetSandboxId() != "session-attach" {
		t.Fatalf("started = %#v", started)
	}
	if output := sent[1].GetOutput(); string(output.GetData()) != "out\n" || output.GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDOUT || output.GetTty() {
		t.Fatalf("stdout output = %#v", output)
	}
	if output := sent[2].GetOutput(); string(output.GetData()) != "err\n" || output.GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		t.Fatalf("stderr output = %#v", output)
	}
	result := sent[3].GetResult()
	if result.GetExitCode() != 7 || result.GetSuccess() || result.GetError() != "boom" {
		t.Fatalf("result = %#v", result)
	}
	if execResult := result.GetExecResult(); execResult.GetStdout() != "out\n" || execResult.GetStderr() != "err\n" || execResult.GetOutput() != "out\nerr\n" || execResult.GetError() != "boom" {
		t.Fatalf("exec result = %#v", execResult)
	}
	if runtime.spec.Command == nil || runtime.spec.Command.Command != "printf" || runtime.spec.Cwd != "/workspace" || runtime.spec.TimeoutMs != 1234 {
		t.Fatalf("runtime spec = %#v", runtime.spec)
	}
}

func TestExecAttachRunnerProjectsInteractionFramesWithoutRPC(t *testing.T) {
	state := execAttachProjectionTestState()
	runner := execAttachRunner{
		state:   state,
		receive: sliceExecAttachReceiver(),
	}
	interaction := &apiAttachInteraction{outputs: []driverpkg.RuntimeOutputFrame{
		{Type: driverpkg.RuntimeOutputStarted},
		{Type: driverpkg.RuntimeOutputStdout, Data: []byte("alpha\n")},
		{Type: driverpkg.RuntimeOutputStderr, Data: []byte("beta\n")},
		{Type: driverpkg.RuntimeOutputResult, Result: &driverpkg.RuntimeResult{ExitCode: 0, Success: true}},
	}}
	var sent []*agentcomposev2.ExecAttachResponse
	runner.send = func(resp *agentcomposev2.ExecAttachResponse) error {
		sent = append(sent, resp)
		return nil
	}

	if err := runner.runInteraction(interaction); err != nil {
		t.Fatalf("runInteraction returned error: %v", err)
	}
	if len(sent) != 4 {
		t.Fatalf("responses = %#v", sent)
	}
	if started := sent[0].GetStarted(); started.GetOperationId() != "exec-projection" || started.GetSandboxId() != "session-projection" {
		t.Fatalf("started = %#v", started)
	}
	if output := sent[1].GetOutput(); output.GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDOUT || string(output.GetData()) != "alpha\n" {
		t.Fatalf("stdout = %#v", output)
	}
	if output := sent[2].GetOutput(); output.GetStream() != agentcomposev2.StdioStream_STDIO_STREAM_STDERR || string(output.GetData()) != "beta\n" {
		t.Fatalf("stderr = %#v", output)
	}
	if result := sent[3].GetResult().GetExecResult(); result.GetStdout() != "alpha\n" || result.GetStderr() != "beta\n" || result.GetOutput() != "alpha\nbeta\n" {
		t.Fatalf("exec result = %#v", result)
	}
}

func TestExecAttachPromptDelegatesToRunAttach(t *testing.T) {
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-attach", VMStatus: domain.VMStatusRunning}}
	store := &apiExecWorkflowSandboxStore{
		sandboxes: map[string]*domain.Sandbox{"session-attach": session},
		vm:        domain.VMState{Driver: "docker"},
	}
	projects := &apiExecWorkflowProjectStore{runs: []domain.ProjectRunRecord{{
		RunID: "run-latest", ProjectID: "project-1", AgentName: "worker", SandboxID: "session-attach",
	}}}
	delegate := &apiExecPromptRunAttachDelegate{}
	handler := NewExecHandler(&appconfig.Config{}, store, projects, func(*domain.Sandbox) (ExecRuntime, error) {
		return &apiExecRuntime{}, nil
	}, delegate)
	var sent []*agentcomposev2.ExecAttachResponse
	err := handler.execAttach(context.Background(), sliceExecAttachReceiver(
		&agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Start{Start: &agentcomposev2.ExecAttachStart{
			Request: &agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "session-attach"}},
			Mode:    agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
			Prompt:  "hi",
		}}},
		&agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_HumanMessage{HumanMessage: &agentcomposev2.AttachHumanMessage{Text: "next"}}},
	), func(resp *agentcomposev2.ExecAttachResponse) error {
		sent = append(sent, resp)
		return nil
	})
	if err != nil {
		t.Fatalf("ExecAttach prompt returned error: %v", err)
	}
	if delegate.start.GetRequest().GetProjectId() != "project-1" || delegate.start.GetRequest().GetAgentName() != "worker" || delegate.start.GetRequest().GetPrompt() != "hi" || delegate.start.GetRequest().GetSandboxId() != "session-attach" {
		t.Fatalf("delegated run start = %#v", delegate.start)
	}
	if delegate.human.GetText() != "next" {
		t.Fatalf("delegated human message = %#v", delegate.human)
	}
	if len(sent) != 2 || sent[0].GetAgentEvent().GetText() != "agent says hi" || !sent[1].GetResult().GetSuccess() {
		t.Fatalf("exec prompt responses = %#v", sent)
	}
}

func TestExecAttachInputFrameMapping(t *testing.T) {
	cases := []struct {
		name string
		req  *agentcomposev2.ExecAttachRequest
		want driverpkg.RuntimeInputFrame
	}{
		{
			name: "stdin",
			req:  &agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Stdin{Stdin: &agentcomposev2.AttachStdin{Data: []byte("hi")}}},
			want: driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: []byte("hi")},
		},
		{
			name: "stdin eof",
			req:  &agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_StdinEof{StdinEof: &agentcomposev2.AttachStdinEOF{}}},
			want: driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdinEOF},
		},
		{
			name: "resize",
			req:  &agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Resize{Resize: &agentcomposev2.AttachResize{TerminalSize: &agentcomposev2.AttachTerminalSize{Rows: 24, Cols: 80}}}},
			want: driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputResize, Rows: 24, Cols: 80},
		},
		{
			name: "signal",
			req:  &agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Signal{Signal: &agentcomposev2.AttachSignal{Signal: " interrupt "}}},
			want: driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputSignal, Signal: driverpkg.RuntimeSignalInterrupt},
		},
		{
			name: "cancel",
			req:  &agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Cancel{Cancel: &agentcomposev2.AttachCancel{Reason: "stop"}}},
			want: driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputCancel, Message: "stop"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := runtimeInputFrameFromExecAttach(tc.req)
			if !ok || string(got.Data) != string(tc.want.Data) || got.Type != tc.want.Type || got.Rows != tc.want.Rows || got.Cols != tc.want.Cols || got.Signal != tc.want.Signal || got.Message != tc.want.Message {
				t.Fatalf("frame = %#v ok=%v, want %#v", got, ok, tc.want)
			}
		})
	}
}

func TestProjectAndRunHandlersStoreBackedWorkflows(t *testing.T) {
	ctx := context.Background()
	store := &apiProjectRunStore{
		projects: []domain.ProjectRecord{{ID: "project-1", Name: "Project", CurrentRevision: 1}},
		agents: []domain.ProjectAgentRecord{
			{ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Revision: 1, Driver: "boxlite", Image: "guest:latest"},
			{ProjectID: "project-1", AgentName: "worker-2", ManagedAgentID: "agent-2", Revision: 1, Driver: "boxlite", Image: "guest:latest"},
		},
		schedulers: []domain.ProjectSchedulerRecord{
			{ProjectID: "project-1", SchedulerID: "scheduler-1", AgentName: "worker", ManagedLoaderID: "loader-1", Revision: 1, Enabled: true, SpecJSON: `{"enabled":true,"display_name":"每日巡检","description":"每天汇总巡检结果","script":"run()"}`, RunCount: 3, LatestRunAt: time.Unix(10, 0), LastError: "failed"},
			{ProjectID: "project-1", SchedulerID: "scheduler-2", AgentName: "worker-2", ManagedLoaderID: "loader-2", Revision: 1, Enabled: true, SpecJSON: `{"enabled":true,"script":"run()"}`},
		},
		loaders: map[string]domain.Loader{
			"loader-1": {Summary: domain.LoaderSummary{ID: "loader-1", Enabled: false}},
			"loader-2": {Summary: domain.LoaderSummary{ID: "loader-2", Enabled: true}},
		},
		revision: domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"variables":[{"name":"PROJECT_SECRET","value":"project-secret","secret":true}],"mcp_servers":[{"name":"remote","headers":[{"name":"Authorization","value":"mcp-secret","secret":true}]}],"octobus_servers":[{"name":"internal","url":"https://octobus.example","token":"octobus-secret"}],"agents":[{"name":"worker","env":[{"name":"AGENT_SECRET","value":"agent-secret","secret":true}]},{"name":"worker-2"}]}`},
		agentRunStates: []domain.ProjectAgentRunState{
			{AgentName: "worker", RunningRunCount: 1, RunningSchedulerRunCount: 2, LatestRunID: "run-1", LatestStatus: domain.ProjectRunStatusFailed, LatestSource: domain.ProjectRunSourceAPI, LatestAt: time.Unix(20, 0)},
			{AgentName: "worker-2", LatestRunID: "run-2", LatestStatus: domain.ProjectRunStatusSucceeded, LatestSource: domain.ProjectRunSourceScheduler, LatestAt: time.Unix(10, 0)},
		},
		runs: map[string]domain.ProjectRunRecord{
			"run-1": {RunID: "run-1", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", SandboxID: strings.Repeat("a", 64), Status: domain.ProjectRunStatusRunning, Source: domain.ProjectRunSourceAPI, ResultJSON: "{}"},
		},
		runEvents: []domain.ProjectRunEventRecord{{ID: "event-1", RunID: "run-1", Sequence: 1, Kind: domain.ProjectRunEventKindUserMessage, Text: "hello", CreatedAt: time.Unix(1, 0)}},
	}
	projectHandler := NewProjectHandler(nil, store, nil)
	projectResp, err := projectHandler.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Name: "Project"}, IncludeSpec: true}))
	if err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}
	if projectResp.Msg.GetProject().GetSummary().GetProjectId() != "project-1" || projectResp.Msg.GetProject().GetSpec() == nil {
		t.Fatalf("project response = %#v", projectResp.Msg.GetProject())
	}
	projectSpec := projectResp.Msg.GetProject().GetSpec()
	if projectSpec.GetVariables()[0].GetValue() != "project-secret" ||
		projectSpec.GetMcpServers()[0].GetHeaders()[0].GetValue() != "mcp-secret" ||
		projectSpec.GetOctobusServers()[0].GetToken() != secretRedactedValue ||
		projectSpec.GetAgents()[0].GetEnv()[0].GetValue() != "agent-secret" {
		t.Fatalf("GetProject changed existing secret behavior or exposed OctoBus token = %#v", projectSpec)
	}
	projectAgent := projectResp.Msg.GetProject().GetAgents()[0]
	if store.agentRunStateCalls != 1 || projectAgent.GetCurrentRun().GetRunningRunCount() != 1 || projectAgent.GetCurrentRun().GetRunningSchedulerRunCount() != 2 || projectAgent.GetLatestRun().GetRunId() != "run-1" || projectAgent.GetHealth() != agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK {
		t.Fatalf("project agent run enrichment calls=%d agent=%#v", store.agentRunStateCalls, projectAgent)
	}
	listProjects, err := projectHandler.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Query: "Project", Limit: 10}))
	if err != nil || len(listProjects.Msg.GetProjects()) != 1 {
		t.Fatalf("ListProjects resp=%#v err=%v", listProjects, err)
	}
	if summary := listProjects.Msg.GetProjects()[0]; summary.GetAgentCount() != 2 || summary.GetSchedulerCount() != 2 {
		t.Fatalf("ListProjects summary counts = agents %d schedulers %d", summary.GetAgentCount(), summary.GetSchedulerCount())
	}
	scheduler, err := projectHandler.GetScheduler(ctx, connect.NewRequest(&agentcomposev2.GetSchedulerRequest{Project: &agentcomposev2.ProjectRef{ProjectId: "project-1"}, AgentName: "worker"}))
	if err != nil || scheduler.Msg.GetScheduler().GetSchedulerId() != "scheduler-1" || scheduler.Msg.GetScheduler().GetDisplayName() != "每日巡检" || scheduler.Msg.GetSpec().GetScript() != "run()" || scheduler.Msg.GetSpec().GetDescription() != "每天汇总巡检结果" {
		t.Fatalf("GetScheduler resp=%#v err=%v", scheduler, err)
	}
	firstSchedulers, err := projectHandler.ListSchedulers(ctx, connect.NewRequest(&agentcomposev2.ListSchedulersRequest{Limit: 1}))
	if err != nil || len(firstSchedulers.Msg.GetSchedulers()) != 1 || firstSchedulers.Msg.GetNextCursor() == "" {
		t.Fatalf("ListSchedulers first page=%#v err=%v", firstSchedulers, err)
	}
	if summary := firstSchedulers.Msg.GetSchedulers()[0]; summary.GetEnabled() || summary.GetRunCount() != 3 || !summary.GetLatestRunAt().AsTime().Equal(time.Unix(10, 0)) || summary.GetLastError() != "failed" || summary.GetDisplayName() != "每日巡检" || summary.GetDescription() != "每天汇总巡检结果" {
		t.Fatalf("ListSchedulers summary=%#v", summary)
	}
	secondSchedulers, err := projectHandler.ListSchedulers(ctx, connect.NewRequest(&agentcomposev2.ListSchedulersRequest{Limit: 1, Cursor: firstSchedulers.Msg.GetNextCursor()}))
	if err != nil || len(secondSchedulers.Msg.GetSchedulers()) != 1 || secondSchedulers.Msg.GetSchedulers()[0].GetSchedulerId() != "scheduler-2" {
		t.Fatalf("ListSchedulers second page=%#v err=%v", secondSchedulers, err)
	}
	delete(store.loaders, "loader-2")
	missingLoaderSchedulers, err := projectHandler.ListSchedulers(ctx, connect.NewRequest(&agentcomposev2.ListSchedulersRequest{Limit: 10}))
	if err != nil || len(missingLoaderSchedulers.Msg.GetSchedulers()) != 2 || !missingLoaderSchedulers.Msg.GetSchedulers()[1].GetEnabled() {
		t.Fatalf("ListSchedulers missing loader fallback=%#v err=%v", missingLoaderSchedulers, err)
	}
	store.loaders["loader-2"] = domain.Loader{Summary: domain.LoaderSummary{ID: "loader-2", Enabled: true}}
	store.projects = append(store.projects, domain.ProjectRecord{ID: "project-2", Name: "Project"})
	if _, err := projectHandler.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Name: "Project"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected ambiguous project error, got %v", err)
	}
	store.projects = store.projects[:1]
	if _, err := projectHandler.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected missing project ref error, got %v", err)
	}

	runHandler := NewRunHandler(nil, store)
	runResp, err := runHandler.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{RunId: "run-1", ProjectId: "project-1"}))
	if err != nil || runResp.Msg.GetRun().GetSummary().GetRunId() != "run-1" {
		t.Fatalf("GetRun resp=%#v err=%v", runResp, err)
	}
	listRuns, err := runHandler.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{ProjectId: "project-1", Limit: 10}))
	if err != nil || len(listRuns.Msg.GetRuns()) != 1 {
		t.Fatalf("ListRuns resp=%#v err=%v", listRuns, err)
	}
	runEvents, err := runHandler.ListRunEvents(ctx, connect.NewRequest(&agentcomposev2.ListRunEventsRequest{RunId: "run-1", Limit: 10}))
	if err != nil || !runEvents.Msg.GetHistoryAvailable() || len(runEvents.Msg.GetEvents()) != 1 {
		t.Fatalf("ListRunEvents resp=%#v err=%v", runEvents, err)
	}
	batchEvents, err := runHandler.ListSandboxRunEvents(ctx, connect.NewRequest(&agentcomposev2.ListSandboxRunEventsRequest{SandboxId: strings.Repeat("a", 64), Limit: 10}))
	if err != nil || len(batchEvents.Msg.GetEvents()) != 1 || len(batchEvents.Msg.GetHistoryAvailableRunIds()) != 1 {
		t.Fatalf("ListSandboxRunEvents resp=%#v err=%v", batchEvents, err)
	}
	stopResp, err := runHandler.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{RunId: "run-1", Reason: "stop"}))
	if err != nil || !stopResp.Msg.GetStopRequested() || stopResp.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_CANCELED {
		t.Fatalf("StopRun resp=%#v err=%v", stopResp, err)
	}
	terminalResp, err := runHandler.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{RunId: "run-1"}))
	if err != nil || terminalResp.Msg.GetStopRequested() {
		t.Fatalf("terminal StopRun resp=%#v err=%v", terminalResp, err)
	}
	if _, err := runHandler.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected run id error, got %v", err)
	}
}

func TestFollowRunLogsStreamsOffsetsTailAndFinal(t *testing.T) {
	tempDir := t.TempDir()
	logPath := tempDir + "/output.txt"
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusSucceeded, LogsPath: logPath},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	all := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1"})
	if len(all) != 2 || all[0].GetData() != "one\ntwo\nthree\n" || all[0].GetOffset() != 14 || !all[1].GetIsFinal() {
		t.Fatalf("all chunks = %#v", all)
	}

	followAll := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", Follow: true})
	if len(followAll) != 2 || followAll[0].GetData() != "one\ntwo\nthree\n" || followAll[0].GetOffset() != 14 || !followAll[1].GetIsFinal() {
		t.Fatalf("follow all chunks = %#v", followAll)
	}

	tail := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", TailLines: 2, Follow: true})
	if len(tail) != 2 || tail[0].GetData() != "two\nthree\n" || tail[0].GetOffset() != 14 || !tail[1].GetIsFinal() {
		t.Fatalf("tail chunks = %#v", tail)
	}

	offset := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", StartOffset: 4})
	if len(offset) != 2 || offset[0].GetData() != "two\nthree\n" || offset[0].GetOffset() != 14 || !offset[1].GetIsFinal() {
		t.Fatalf("offset chunks = %#v", offset)
	}
}

func TestFollowRunLogsMissingLogFileReturnsEmptyFinalForTerminalRun(t *testing.T) {
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusFailed, LogsPath: t.TempDir() + "/missing.txt"},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	chunks := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", Follow: true})
	if len(chunks) != 1 || !chunks[0].GetIsFinal() || chunks[0].GetData() != "" || chunks[0].GetRunStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED {
		t.Fatalf("missing log chunks = %#v", chunks)
	}
}

func TestFollowRunLogsRejectsProjectMismatch(t *testing.T) {
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusSucceeded},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{ProjectId: "project-2", RunId: "run-1"}))
	if err != nil {
		t.Fatalf("FollowRunLogs returned setup error: %v", err)
	}
	for stream.Receive() {
		t.Fatalf("unexpected chunk: %#v", stream.Msg())
	}
	if code := connect.CodeOf(stream.Err()); code != connect.CodeNotFound {
		t.Fatalf("FollowRunLogs code = %s, want %s (err=%v)", code, connect.CodeNotFound, stream.Err())
	}
}

func TestFollowRunLogsHubBacklogDedupeAndTerminalSupplement(t *testing.T) {
	tempDir := t.TempDir()
	logPath := filepath.Join(tempDir, "output.txt")
	if err := os.WriteFile(logPath, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusRunning, LogsPath: logPath},
	}}
	hub := runs.NewRunLogHub()
	client, closeServer := newRunHandlerTestClient(t, NewRunHandlerWithRunLogHub(nil, store, hub))
	defer closeServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.FollowRunLogs(ctx, connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", Follow: true}))
	if err != nil {
		t.Fatalf("FollowRunLogs setup error: %v", err)
	}
	first := receiveRunLogChunk(t, stream, time.Second)
	if first.GetData() != "old\n" || first.GetOffset() != 4 || first.GetIsFinal() {
		t.Fatalf("first chunk = %#v", first)
	}

	hub.Publish(runs.RunLogEvent{RunID: "run-1", Data: "old\n", Offset: 4, CreatedAt: time.Now().UTC()})
	newOffset := appendRunLogFixture(t, logPath, "new\n")
	started := time.Now()
	hub.Publish(runs.RunLogEvent{RunID: "run-1", Data: "new\n", Offset: newOffset, CreatedAt: time.Now().UTC()})
	second := receiveRunLogChunk(t, stream, time.Second)
	if second.GetData() != "new\n" || second.GetOffset() != newOffset || second.GetIsFinal() {
		t.Fatalf("second chunk = %#v", second)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("hub-delivered chunk took %s, want below polling interval", elapsed)
	}

	fallbackOffset := appendRunLogFixture(t, logPath, "missed\n")
	fallback := receiveRunLogChunk(t, stream, time.Second)
	if fallback.GetData() != "missed\n" || fallback.GetOffset() != fallbackOffset {
		t.Fatalf("polling fallback chunk = %#v", fallback)
	}

	finalOffset := appendRunLogFixture(t, logPath, "done\n")
	run, err := store.GetProjectRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	run.Status = domain.ProjectRunStatusSucceeded
	if _, err := store.UpdateProjectRun(context.Background(), run); err != nil {
		t.Fatalf("UpdateProjectRun returned error: %v", err)
	}
	terminalData := receiveRunLogChunk(t, stream, time.Second)
	if terminalData.GetData() != "done\n" || terminalData.GetOffset() != finalOffset {
		t.Fatalf("terminal supplement chunk = %#v", terminalData)
	}
	final := receiveRunLogChunk(t, stream, time.Second)
	if !final.GetIsFinal() || final.GetOffset() != finalOffset || final.GetRunStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("final chunk = %#v", final)
	}
}

func appendRunLogFixture(t *testing.T, path, text string) uint64 {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open log fixture: %v", err)
	}
	defer func() { _ = file.Close() }()
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("seek log fixture: %v", err)
	}
	n, err := file.WriteString(text)
	if err != nil {
		t.Fatalf("append log fixture: %v", err)
	}
	return uint64(offset) + uint64(n)
}

func receiveRunLogChunk(t *testing.T, stream *connect.ServerStreamForClient[agentcomposev2.RunLogChunk], timeout time.Duration) *agentcomposev2.RunLogChunk {
	t.Helper()
	type result struct {
		chunk *agentcomposev2.RunLogChunk
		err   error
	}
	done := make(chan result, 1)
	go func() {
		if stream.Receive() {
			done <- result{chunk: stream.Msg()}
			return
		}
		done <- result{err: stream.Err()}
	}()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("FollowRunLogs stream error: %v", result.err)
		}
		if result.chunk == nil {
			t.Fatalf("FollowRunLogs stream ended")
		}
		return result.chunk
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for run log chunk")
		return nil
	}
}

func newRunHandlerTestClient(t *testing.T, handler *RunHandler) (agentcomposev2connect.RunServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, serviceHandler := agentcomposev2connect.NewRunServiceHandler(handler)
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL), server.Close
}

func collectRunLogChunks(t *testing.T, client agentcomposev2connect.RunServiceClient, req *agentcomposev2.FollowRunLogsRequest) []*agentcomposev2.RunLogChunk {
	t.Helper()
	stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("FollowRunLogs setup error: %v", err)
	}
	var chunks []*agentcomposev2.RunLogChunk
	for stream.Receive() {
		chunks = append(chunks, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("FollowRunLogs stream error: %v", err)
	}
	return chunks
}

type apiExecSandboxStore struct {
	sandbox *domain.Sandbox
	vm      domain.VMState
}

func (s *apiExecSandboxStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	if s.sandbox == nil {
		return nil, sql.ErrNoRows
	}
	return s.sandbox, nil
}

func (s *apiExecSandboxStore) GetVMState(string) (domain.VMState, error) {
	return s.vm, nil
}

type apiExecProjectStore struct {
	err error
}

func (s apiExecProjectStore) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return domain.ProjectRecord{}, s.err
}

func (s apiExecProjectStore) GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, s.err
}

func (s apiExecProjectStore) ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{}, s.err
}

func (s apiExecProjectStore) ListProjectSandboxRuns(context.Context, domain.ProjectSandboxRelationFilter) ([]domain.ProjectRunRecord, error) {
	return nil, s.err
}

type apiExecWorkflowSandboxStore struct {
	sandboxes map[string]*domain.Sandbox
	vm        domain.VMState
}

func (s *apiExecWorkflowSandboxStore) GetSandbox(_ context.Context, id string) (*domain.Sandbox, error) {
	sandbox := s.sandboxes[id]
	if sandbox == nil {
		return nil, sql.ErrNoRows
	}
	return sandbox, nil
}

func (s *apiExecWorkflowSandboxStore) GetVMState(string) (domain.VMState, error) {
	return s.vm, nil
}

type apiExecWorkflowProjectStore struct {
	projects []domain.ProjectRecord
	runs     []domain.ProjectRunRecord
}

func (s *apiExecWorkflowProjectStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, error) {
	for _, project := range s.projects {
		if project.ID == id {
			return project, nil
		}
	}
	return domain.ProjectRecord{}, sql.ErrNoRows
}

func (s *apiExecWorkflowProjectStore) GetProjectRun(_ context.Context, id string) (domain.ProjectRunRecord, error) {
	for _, run := range s.runs {
		if run.RunID == id {
			return run, nil
		}
	}
	return domain.ProjectRunRecord{}, sql.ErrNoRows
}

func (s *apiExecWorkflowProjectStore) ListProjects(_ context.Context, options domain.ProjectListOptions) (domain.ProjectListResult, error) {
	var projects []domain.ProjectRecord
	for _, project := range s.projects {
		if options.Query == "" || strings.Contains(project.Name, options.Query) {
			projects = append(projects, project)
		}
	}
	return domain.ProjectListResult{Projects: projects, TotalCount: len(projects)}, nil
}

func (s *apiExecWorkflowProjectStore) ListProjectSandboxRuns(_ context.Context, filter domain.ProjectSandboxRelationFilter) ([]domain.ProjectRunRecord, error) {
	var runs []domain.ProjectRunRecord
	for _, run := range s.runs {
		if filter.ProjectID != "" && run.ProjectID != filter.ProjectID {
			continue
		}
		if filter.AgentName != "" && run.AgentName != filter.AgentName {
			continue
		}
		if filter.SandboxID != "" && run.SandboxID != filter.SandboxID {
			continue
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func newExecAttachTestHandler(t *testing.T, runtime ExecRuntime) *ExecHandler {
	t.Helper()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-attach", VMStatus: domain.VMStatusRunning}}
	store := &apiExecSandboxStore{sandbox: session, vm: domain.VMState{Driver: "docker"}}
	return NewExecHandler(&appconfig.Config{}, store, apiExecProjectStore{}, func(*domain.Sandbox) (ExecRuntime, error) {
		return runtime, nil
	})
}

func execAttachStartRequest() *agentcomposev2.ExecAttachRequest {
	return &agentcomposev2.ExecAttachRequest{Frame: &agentcomposev2.ExecAttachRequest_Start{Start: &agentcomposev2.ExecAttachStart{
		Request: &agentcomposev2.ExecRequest{
			Target:    &agentcomposev2.ExecRequest_SandboxId{SandboxId: "session-attach"},
			Command:   &agentcomposev2.ExecCommand{Command: "printf", Args: []string{"hi"}},
			Cwd:       "/workspace",
			TimeoutMs: 1234,
		},
	}}}
}

func execAttachProjectionTestState() *execAttachState {
	return &execAttachState{
		execID: "exec-projection",
		runID:  "run-projection",
		cwd:    "/workspace",
		request: &agentcomposev2.ExecRequest{
			Target:  &agentcomposev2.ExecRequest_SandboxId{SandboxId: "session-projection"},
			Command: &agentcomposev2.ExecCommand{Command: "printf"},
			Cwd:     "/workspace",
		},
		sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-projection", VMStatus: domain.VMStatusRunning}},
		tty:     true,
	}
}

func sliceExecAttachReceiver(items ...*agentcomposev2.ExecAttachRequest) execAttachReceiver {
	index := 0
	return func() (*agentcomposev2.ExecAttachRequest, error) {
		if index >= len(items) {
			return nil, io.EOF
		}
		item := items[index]
		index++
		return item, nil
	}
}

type apiExecPromptRunAttachDelegate struct {
	start *agentcomposev2.RunAttachStart
	human *agentcomposev2.AttachHumanMessage
}

func (d *apiExecPromptRunAttachDelegate) RunProjectCommandAttach(_ context.Context, receive runs.RunAttachReceiver, send runs.RunAttachSender) error {
	first, err := receive()
	if err != nil {
		return err
	}
	d.start = first.GetStart()
	second, err := receive()
	if err != nil {
		return err
	}
	d.human = second.GetHumanMessage()
	if err := send(&agentcomposev2.RunAttachResponse{Frame: &agentcomposev2.RunAttachResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{Text: "agent says hi"}}}); err != nil {
		return err
	}
	return send(&agentcomposev2.RunAttachResponse{Frame: &agentcomposev2.RunAttachResponse_Result{Result: &agentcomposev2.AttachResult{Success: true}}})
}

type apiExecRuntime struct {
	spec domain.ExecSpec
}

func (r *apiExecRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.spec = spec
	writer(domain.ExecChunk{Text: "$ echo hi\n"})
	writer(domain.ExecChunk{Text: "hi\n"})
	payload := apiRuntimeCommandPayload(domain.RuntimeCommandResult{Stdout: "hi\n", Output: "hi\n", ExitCode: 0, Success: true})
	writer(domain.ExecChunk{Text: payload})
	return domain.ExecResult{Stdout: payload, Output: "$ echo hi\nhi\n" + payload, ExitCode: 0, Success: true}, nil
}

type apiAttachRuntime struct {
	apiExecRuntime
	spec        driverpkg.RuntimeStartSpec
	interaction driverpkg.RuntimeInteraction
}

func (r *apiAttachRuntime) OpenInteraction(_ context.Context, _ *domain.Sandbox, _ domain.VMState, spec driverpkg.RuntimeStartSpec) (driverpkg.RuntimeInteraction, error) {
	r.spec = spec
	return r.interaction, nil
}

type apiAttachInteraction struct {
	outputs []driverpkg.RuntimeOutputFrame
	index   int
}

func (i *apiAttachInteraction) Send(driverpkg.RuntimeInputFrame) error {
	return nil
}

func (i *apiAttachInteraction) CloseSend() error {
	return nil
}

func (i *apiAttachInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	if i.index >= len(i.outputs) {
		return driverpkg.RuntimeOutputFrame{}, io.EOF
	}
	frame := i.outputs[i.index]
	i.index++
	return frame, nil
}

func (i *apiAttachInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{}, nil
}

func apiRuntimeCommandPayload(result domain.RuntimeCommandResult) string {
	data, _ := json.Marshal(result)
	return execution.CommandResultPrefix + string(data) + "\n"
}

type apiSandboxStore struct {
	session   *domain.Sandbox
	vmState   domain.VMState
	removeErr error
}

func (s *apiSandboxStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.session, nil
}

func (s *apiSandboxStore) RemoveSandbox(context.Context, string) error {
	return s.removeErr
}

func (s *apiSandboxStore) GetVMState(string) (domain.VMState, error) {
	return s.vmState, nil
}

type apiStatsRuntime struct {
	stats     domain.SandboxStats
	sessionID string
	vmState   domain.VMState
}

func (r *apiStatsRuntime) Stats(_ context.Context, session *domain.Sandbox, vmState domain.VMState) (domain.SandboxStats, error) {
	if session != nil {
		r.sessionID = session.Summary.ID
	}
	r.vmState = vmState
	return r.stats, nil
}

type apiProjectRunStore struct {
	mu                 sync.Mutex
	projects           []domain.ProjectRecord
	agents             []domain.ProjectAgentRecord
	schedulers         []domain.ProjectSchedulerRecord
	revision           domain.ProjectRevisionRecord
	agentRunStates     []domain.ProjectAgentRunState
	agentRunStateCalls int
	runs               map[string]domain.ProjectRunRecord
	runEvents          []domain.ProjectRunEventRecord
	loaders            map[string]domain.Loader
}

func (s *apiProjectRunStore) GetProject(_ context.Context, projectID string) (domain.ProjectRecord, error) {
	for _, project := range s.projects {
		if project.ID == projectID {
			return project, nil
		}
	}
	return domain.ProjectRecord{}, sql.ErrNoRows
}

func (s *apiProjectRunStore) ListProjects(_ context.Context, _ domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Projects: s.projects, TotalCount: len(s.projects)}, nil
}

func (s *apiProjectRunStore) ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error) {
	return s.agents, nil
}

func (s *apiProjectRunStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	return s.schedulers, nil
}

func (s *apiProjectRunStore) ListProjectSchedulersPage(_ context.Context, query, afterKey string, limit int) ([]domain.ProjectSchedulerRecord, error) {
	var items []domain.ProjectSchedulerRecord
	for _, scheduler := range s.schedulers {
		key := scheduler.ProjectID + "\x00" + scheduler.AgentName + "\x00" + scheduler.SchedulerID
		if key > afterKey {
			items = append(items, scheduler)
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *apiProjectRunStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *apiProjectRunStore) GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error) {
	if len(s.agents) == 0 {
		return domain.ProjectAgentRecord{}, sql.ErrNoRows
	}
	return s.agents[0], nil
}

func (s *apiProjectRunStore) GetManagedAgentDefinition(context.Context, string) (runs.ManagedAgentDefinition, error) {
	return runs.ManagedAgentDefinition{ID: "agent-1", Enabled: true, ManagedProjectID: "project-1", ManagedAgentName: "worker"}, nil
}

func (s *apiProjectRunStore) GetLoader(_ context.Context, loaderID string) (domain.Loader, error) {
	loader, ok := s.loaders[loaderID]
	if !ok {
		return domain.Loader{}, sql.ErrNoRows
	}
	return loader, nil
}

func (s *apiProjectRunStore) SetLoaderEnabled(_ context.Context, loaderID string, enabled bool) error {
	loader, ok := s.loaders[loaderID]
	if !ok {
		return sql.ErrNoRows
	}
	loader.Summary.Enabled = enabled
	s.loaders[loaderID] = loader
	return nil
}

func (s *apiProjectRunStore) SetLoaderTriggerEnabled(context.Context, string, string, bool) error {
	return nil
}

func (s *apiProjectRunStore) ListLoaderEvents(context.Context, string, int) ([]domain.LoaderEvent, error) {
	return nil, nil
}

func (s *apiProjectRunStore) CreateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.RunID] = run
	return run, nil
}

func (s *apiProjectRunStore) CreateProjectRunWithEvents(ctx context.Context, run domain.ProjectRunRecord, events []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	created, err := s.CreateProjectRun(ctx, run)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	s.mu.Lock()
	s.runEvents = append(s.runEvents, events...)
	s.mu.Unlock()
	return created, nil
}

func (s *apiProjectRunStore) GetProjectRun(_ context.Context, runID string) (domain.ProjectRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[runID]
	if !ok {
		return domain.ProjectRunRecord{}, sql.ErrNoRows
	}
	return run, nil
}

func (s *apiProjectRunStore) UpdateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.RunID] = run
	return run, nil
}

func (s *apiProjectRunStore) UpdateProjectRunWithEvents(ctx context.Context, run domain.ProjectRunRecord, events []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	updated, err := s.UpdateProjectRun(ctx, run)
	if err != nil {
		return domain.ProjectRunRecord{}, err
	}
	s.mu.Lock()
	s.runEvents = append(s.runEvents, events...)
	s.mu.Unlock()
	return updated, nil
}

func (s *apiProjectRunStore) ListProjectRunsByOptions(_ context.Context, _ domain.ProjectRunListOptions) ([]domain.ProjectRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]domain.ProjectRunRecord, 0, len(s.runs))
	for _, run := range s.runs {
		items = append(items, run)
	}
	return items, nil
}

func (s *apiProjectRunStore) ListProjectAgentRunStates(_ context.Context, _ string) ([]domain.ProjectAgentRunState, error) {
	s.agentRunStateCalls++
	return s.agentRunStates, nil
}

func (s *apiProjectRunStore) ListProjectRunsForSandbox(_ context.Context, sandboxID string) ([]domain.ProjectRunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []domain.ProjectRunRecord
	for _, run := range s.runs {
		if run.SandboxID == sandboxID {
			items = append(items, run)
		}
	}
	return items, nil
}

func (s *apiProjectRunStore) ListProjectRunEvents(_ context.Context, runID string, after uint64, limit int) ([]domain.ProjectRunEventRecord, error) {
	var items []domain.ProjectRunEventRecord
	for _, event := range s.runEvents {
		if event.RunID == runID && event.Sequence > after {
			items = append(items, event)
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func (s *apiProjectRunStore) HasProjectRunEvents(_ context.Context, runID string) (bool, error) {
	for _, event := range s.runEvents {
		if event.RunID == runID {
			return true, nil
		}
	}
	return false, nil
}

func (s *apiProjectRunStore) ListProjectRunEventRunIDsForSandbox(_ context.Context, sandboxID string) ([]string, error) {
	allowed := map[string]bool{}
	for _, run := range s.runs {
		if run.SandboxID == sandboxID {
			allowed[run.RunID] = true
		}
	}
	seen := map[string]bool{}
	var runIDs []string
	for _, event := range s.runEvents {
		if allowed[event.RunID] && !seen[event.RunID] {
			seen[event.RunID] = true
			runIDs = append(runIDs, event.RunID)
		}
	}
	return runIDs, nil
}

func (s *apiProjectRunStore) ListProjectRunEventsForSandbox(_ context.Context, sandboxID string, _ time.Time, _ string, _ uint64, limit int) ([]domain.ProjectRunEventRecord, error) {
	allowed := map[string]bool{}
	for _, run := range s.runs {
		if run.SandboxID == sandboxID {
			allowed[run.RunID] = true
		}
	}
	var items []domain.ProjectRunEventRecord
	for _, event := range s.runEvents {
		if allowed[event.RunID] {
			items = append(items, event)
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
