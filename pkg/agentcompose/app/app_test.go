package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/cleanup"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestSetupRegistersServiceGraph(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sandboxes"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("SANDBOX_START_TIMEOUT", "1s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "1s")
	t.Setenv("JUPYTER_PROXY_BASE", "/agent-compose/jupyter/")
	t.Setenv("LLM_API_ENDPOINT", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	do.ProvideValue(di, echo.New())
	Setup(di)
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		_ = StopBackground(shutdownCtx, di)
	})
	cancel()

	app := do.MustInvoke[*echo.Echo](di)
	if len(app.Routes()) == 0 {
		t.Fatalf("expected Setup to register routes")
	}
	for _, route := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/agentcompose.v2.ProjectService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.RunService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ExecService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.ImageService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.CacheService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.VolumeService/*"},
		{method: http.MethodPost, path: "/agentcompose.v2.SandboxService/*"},
		{method: http.MethodGet, path: "/agent-compose/jupyter/:sessionID"},
		{method: http.MethodPost, path: "/agent-compose/jupyter/:sessionID/*"},
	} {
		if !hasEchoRoute(app, route.method, route.path) {
			t.Fatalf("%s %s route was not registered", route.method, route.path)
		}
	}
	config := do.MustInvoke[*appconfig.Config](di)
	volumeManager := do.MustInvoke[*volumes.Manager](di)
	if volumeManager == nil || volumeManager.Drivers[domain.VolumeDriverLocal] == nil {
		t.Fatalf("volume manager was not registered with local driver: %#v", volumeManager)
	}
	req := httptest.NewRequest(http.MethodGet, strings.TrimRight(config.JupyterProxyBasePath, "/")+"/missing", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("proxy route status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
}

func TestNewCleanupRunnerSeparatesWorkspaceAndSandboxRetentionPolicies(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sandboxes"))
	t.Setenv("IMAGE_CACHE_ROOT", filepath.Join(root, "images"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("WORKSPACE_CLEANUP_TTL", "12h")
	t.Setenv("SANDBOX_RETENTION_TTL", "24h")
	t.Setenv("LLM_API_ENDPOINT", "")

	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, context.Background())
	do.ProvideValue(di, slog.Default())
	RegisterDependencies(di)
	runner, err := NewCleanupRunner(di)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.Policies) != 4 {
		t.Fatalf("cleanup policies = %#v, want transient snapshots, workspace, sandbox retention, and image cache", runner.Policies)
	}
	if runner.Policies[1].TTL != 12*time.Hour {
		t.Fatalf("workspace cleanup TTL = %s", runner.Policies[1].TTL)
	}
	if _, ok := runner.Policies[1].Cleaner.(*sandboxes.WorkspaceCleaner); !ok {
		t.Fatalf("workspace cleanup cleaner = %T", runner.Policies[1].Cleaner)
	}
	if runner.Policies[2].TTL != 24*time.Hour {
		t.Fatalf("sandbox retention TTL = %s", runner.Policies[2].TTL)
	}
	if _, ok := runner.Policies[2].Cleaner.(*sandboxes.SandboxRetentionCleaner); !ok {
		t.Fatalf("sandbox retention cleaner = %T", runner.Policies[2].Cleaner)
	}
}

func TestStartBackgroundConstructsCleanupBeforeScheduler(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sandboxes"))
	t.Setenv("IMAGE_CACHE_ROOT", filepath.Join(root, "images"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("LLM_API_ENDPOINT", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	RegisterDependencies(di)

	var constructionOrder []string
	do.Override(di, func(di do.Injector) (*cleanup.Runner, error) {
		constructionOrder = append(constructionOrder, "cleanup")
		return NewCleanupRunner(di)
	})
	do.Override(di, func(di do.Injector) (*schedulers.Controller, error) {
		constructionOrder = append(constructionOrder, "scheduler")
		return NewSchedulerController(di)
	})
	do.Override(di, func(di do.Injector) (*sandboxes.DeletionRecovery, error) {
		constructionOrder = append(constructionOrder, "recovery")
		return NewDeletionRecovery(di)
	})

	if err := StartBackground(di); err != nil {
		t.Fatalf("StartBackground returned error: %v", err)
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := StopBackground(shutdownCtx, di); err != nil {
		t.Fatalf("StopBackground returned error: %v", err)
	}
	if len(constructionOrder) < 3 || constructionOrder[0] != "cleanup" || constructionOrder[1] != "scheduler" || constructionOrder[2] != "recovery" {
		t.Fatalf("background construction order = %v, want cleanup before scheduler before recovery", constructionOrder)
	}
}

func TestAppWorkspaceProvisionerSingletonAndRequired(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sandboxes"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("LLM_API_ENDPOINT", "")

	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, context.Background())
	do.ProvideValue(di, slog.Default())
	RegisterDependencies(di)

	provisioner := do.MustInvoke[*workspaces.Provisioner](di)
	if provisioner == nil {
		t.Fatal("workspace Provisioner was not registered")
	}
	if again := do.MustInvoke[*workspaces.Provisioner](di); again != provisioner {
		t.Fatalf("Provisioner singleton changed: first=%p second=%p", provisioner, again)
	}
	ensurer := do.MustInvoke[workspaces.WorkspaceEnsurer](di)
	if ensurer != provisioner {
		t.Fatalf("WorkspaceEnsurer = %p, want Provisioner %p", ensurer, provisioner)
	}
	if again := do.MustInvoke[workspaces.WorkspaceEnsurer](di); again != ensurer {
		t.Fatalf("WorkspaceEnsurer singleton changed: first=%p second=%p", ensurer, again)
	}

	if bridge := do.MustInvoke[*adapters.SandboxRPCBridge](di); bridge == nil {
		t.Fatal("SandboxRPCBridge did not resolve with WorkspaceEnsurer")
	}
	if runner := do.MustInvoke[*adapters.SchedulerSandboxRunner](di); runner == nil {
		t.Fatal("SchedulerSandboxRunner did not resolve with WorkspaceEnsurer")
	}
	if controller := do.MustInvoke[*runs.Controller](di); controller == nil {
		t.Fatal("runs.Controller did not resolve with WorkspaceEnsurer")
	}

	contract := reflect.TypeOf((*workspaces.WorkspaceEnsurer)(nil)).Elem()
	if contract.NumMethod() != 1 || contract.Method(0).Name != "Ensure" {
		t.Fatalf("WorkspaceEnsurer methods = %v, want only Ensure", contract)
	}
	if err := do.As[*workspaces.Provisioner, workspaces.WorkspaceEnsurer](do.New()); err == nil {
		t.Fatal("WorkspaceEnsurer alias registered without a Provisioner")
	}
}

func TestRegisterUsesSandboxRootAndInitializesConfigStoreSchema(t *testing.T) {
	root := t.TempDir()
	legacyRoot := filepath.Join(root, "sessions")
	if err := os.MkdirAll(legacyRoot, 0o755); err != nil {
		t.Fatalf("create legacy root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "metadata.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
	t.Setenv("DATA_ROOT", root)
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("LLM_API_ENDPOINT", "")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	do.ProvideValue(di, echo.New())

	Register(di)
	cancel()
	config := do.MustInvoke[*appconfig.Config](di)
	wantRoot := legacyRoot
	if config.SandboxRoot != wantRoot {
		t.Fatalf("SandboxRoot = %q, want %q", config.SandboxRoot, wantRoot)
	}
	if info, err := os.Stat(filepath.Join(root, "data.db")); err != nil || info.IsDir() {
		t.Fatalf("data.db stat = %v/%v, want database file", info, err)
	}
}

func TestCacheServiceRouteUsesRuntimeCacheController(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", root)
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "sandboxes"))
	t.Setenv("IMAGE_CACHE_ROOT", filepath.Join(root, "images"))
	t.Setenv("RUNTIME_DRIVER", driverpkg.RuntimeDriverDocker)
	t.Setenv("DOCKER_IMAGE", "guest:latest")
	t.Setenv("SANDBOX_START_TIMEOUT", "1s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "1s")
	t.Setenv("JUPYTER_PROXY_BASE", "/agent-compose/jupyter/")
	t.Setenv("LLM_API_ENDPOINT", "")

	materializedRootFS := filepath.Join(root, "image-cache", "sha256-test", "rootfs")
	if err := os.MkdirAll(materializedRootFS, 0o755); err != nil {
		t.Fatalf("create materialized rootfs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(materializedRootFS, "layer.txt"), []byte("cache data"), 0o644); err != nil {
		t.Fatalf("write materialized fixture: %v", err)
	}

	ctx := context.Background()
	di := do.New()
	appconfig.Setup(di)
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, slog.Default())
	do.ProvideValue(di, echo.New())
	Register(di)

	server := httptest.NewServer(do.MustInvoke[*echo.Echo](di))
	defer server.Close()

	client := agentcomposev2connect.NewCacheServiceClient(server.Client(), server.URL)
	listResp, err := client.ListCaches(ctx, connect.NewRequest(&agentcomposev2.ListCachesRequest{
		Filter: &agentcomposev2.CacheFilter{
			Domain: agentcomposev2.CacheDomain_CACHE_DOMAIN_MATERIALIZED_IMAGE_CACHE,
			Status: agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED,
		},
	}))
	if err != nil {
		t.Fatalf("ListCaches returned error: %v", err)
	}
	var cacheID string
	for _, item := range listResp.Msg.GetCaches() {
		if item.GetPath() == materializedRootFS {
			cacheID = item.GetCacheId()
			if item.GetStatus() != agentcomposev2.CacheStatus_CACHE_STATUS_ORPHANED || !item.GetRemovable() {
				t.Fatalf("listed item status=%s removable=%v, want orphaned removable", item.GetStatus(), item.GetRemovable())
			}
			break
		}
	}
	if cacheID == "" {
		t.Fatalf("materialized rootfs fixture was not listed: %#v", listResp.Msg.GetCaches())
	}

	inspectResp, err := client.InspectCache(ctx, connect.NewRequest(&agentcomposev2.InspectCacheRequest{CacheId: cacheID}))
	if err != nil {
		t.Fatalf("InspectCache returned error: %v", err)
	}
	if inspectResp.Msg.GetCache().GetPath() != materializedRootFS {
		t.Fatalf("InspectCache path = %q, want %q", inspectResp.Msg.GetCache().GetPath(), materializedRootFS)
	}

	pruneResp, err := client.PruneCaches(ctx, connect.NewRequest(&agentcomposev2.PruneCachesRequest{
		Filter: &agentcomposev2.CacheFilter{CacheId: cacheID},
	}))
	if err != nil {
		t.Fatalf("PruneCaches dry-run returned error: %v", err)
	}
	if !pruneResp.Msg.GetDryRun() || len(pruneResp.Msg.GetMatched()) != 1 || len(pruneResp.Msg.GetRemoved()) != 0 {
		t.Fatalf("PruneCaches dry-run response = %#v", pruneResp.Msg)
	}
	if _, err := os.Stat(materializedRootFS); err != nil {
		t.Fatalf("dry-run removed materialized rootfs: %v", err)
	}

	removeDryRunResp, err := client.RemoveCache(ctx, connect.NewRequest(&agentcomposev2.RemoveCacheRequest{CacheId: cacheID}))
	if err != nil {
		t.Fatalf("RemoveCache dry-run returned error: %v", err)
	}
	if !removeDryRunResp.Msg.GetDryRun() || len(removeDryRunResp.Msg.GetMatched()) != 1 || len(removeDryRunResp.Msg.GetRemoved()) != 0 {
		t.Fatalf("RemoveCache dry-run response = %#v", removeDryRunResp.Msg)
	}
	if _, err := os.Stat(materializedRootFS); err != nil {
		t.Fatalf("dry-run remove deleted materialized rootfs: %v", err)
	}

	removeResp, err := client.RemoveCache(ctx, connect.NewRequest(&agentcomposev2.RemoveCacheRequest{
		CacheId: cacheID,
		Force:   true,
	}))
	if err != nil {
		t.Fatalf("RemoveCache force returned error: %v", err)
	}
	if removeResp.Msg.GetDryRun() || len(removeResp.Msg.GetRemoved()) != 1 || removeResp.Msg.GetRemoved()[0] != cacheID {
		t.Fatalf("RemoveCache force response = %#v", removeResp.Msg)
	}
	if _, err := os.Stat(materializedRootFS); !os.IsNotExist(err) {
		t.Fatalf("materialized rootfs still exists after force remove, stat err=%v", err)
	}
}

func TestRunAgentRequestFromProtoPreservesCommand(t *testing.T) {
	req := runAgentRequestFromProto(&agentcomposev2.RunAgentRequest{
		ProjectId:   "project-1",
		AgentName:   "worker",
		Prompt:      "prompt",
		Command:     "echo hi",
		TriggerId:   "trigger-1",
		PayloadJson: `{"input":true}`,
		Driver:      "microsandbox",
		Jupyter:     &agentcomposev2.RunJupyterSpec{Enabled: true, Expose: true},
		Volumes:     []*agentcomposev2.VolumeMountSpec{{Type: agentcomposev2.VolumeMountType_VOLUME_MOUNT_TYPE_BIND, Source: "./fixtures", Target: "/fixtures", ReadOnly: true}},
	})
	if req.ProjectID != "project-1" || req.AgentName != "worker" || req.Prompt != "prompt" || req.Command != "echo hi" || req.TriggerID != "trigger-1" || req.PayloadJSON != `{"input":true}` || req.Driver != "microsandbox" {
		t.Fatalf("mapped request = %#v", req)
	}
	if req.Jupyter == nil || !req.Jupyter.GetEnabled() || !req.Jupyter.GetExpose() {
		t.Fatalf("mapped jupyter request = %#v", req.Jupyter)
	}
	if len(req.Volumes) != 1 || req.Volumes[0].Source != "./fixtures" || !req.Volumes[0].ReadOnly {
		t.Fatalf("mapped volumes = %#v", req.Volumes)
	}
}

func TestApplyProjectValidationIssuesOmitProjectAndRevision(t *testing.T) {
	handler := projectControllerDelegate{controller: projects.NewController(projects.ControllerDependencies{})}
	resp, err := handler.ApplyProject(context.Background(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if len(resp.Msg.GetIssues()) == 0 {
		t.Fatalf("expected validation issues, got %#v", resp.Msg)
	}
	if resp.Msg.GetProject() != nil || resp.Msg.GetRevision() != nil {
		t.Fatalf("validation failure project=%#v revision=%#v", resp.Msg.GetProject(), resp.Msg.GetRevision())
	}
}

func TestStopProjectSandboxUsesInternalStopSemantics(t *testing.T) {
	sandboxID := "sandbox-1"
	store := &projectStopSandboxStore{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{
			ID:       sandboxID,
			VMStatus: domain.VMStatusRunning,
		}},
	}
	driver := &projectStopSandboxDriver{}
	streams := &projectStopSandboxStreams{}

	if err := stopProjectSandbox(context.Background(), stopProjectSandboxDeps{Store: store, Driver: driver, Streams: streams}, store.session); err != nil {
		t.Fatalf("stopProjectSandbox returned error: %v", err)
	}
	if driver.stopCount != 1 {
		t.Fatalf("StopSandboxVM calls = %d, want 1", driver.stopCount)
	}
	if store.updated == nil || store.updated.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("updated sandbox = %#v, want stopped", store.updated)
	}
	if len(store.events) != 1 || store.events[0].Type != "sandbox.stopped" || store.events[0].Message != "sandbox stopped and runtime retained" {
		t.Fatalf("events = %#v, want one sandbox.stopped event", store.events)
	}
	if streams.updatedCount != 1 || streams.eventCount != 1 {
		t.Fatalf("stream notifications updated=%d events=%d, want 1/1", streams.updatedCount, streams.eventCount)
	}
}

func TestStopProjectSandboxRuntimeReleaseFailureFinalizesConfirmedStop(t *testing.T) {
	releaseErr := errors.New("runtime release failed")
	store := &projectStopSandboxStore{
		session: &domain.Sandbox{
			Summary:              domain.SandboxSummary{ID: "sandbox-release-failure", VMStatus: domain.VMStatusRunning},
			StoppedRuntimePolicy: domain.StoppedRuntimePolicyRemove,
		},
	}
	driver := &projectStopSandboxDriver{releaseErr: releaseErr}
	streams := &projectStopSandboxStreams{}

	if err := stopProjectSandbox(context.Background(), stopProjectSandboxDeps{SandboxRoot: t.TempDir(), Store: store, Driver: driver, Streams: streams}, store.session); !errors.Is(err, releaseErr) {
		t.Fatalf("stopProjectSandbox error = %v, want %v", err, releaseErr)
	}
	if driver.stopCount != 1 || driver.releaseCount != 1 {
		t.Fatalf("stop/release calls = %d/%d, want 1/1", driver.stopCount, driver.releaseCount)
	}
	if store.updated == nil || store.updated.Summary.VMStatus != domain.VMStatusStopped || sandboxes.EffectiveStoppedRuntimeState(store.updated) != domain.StoppedRuntimeStateReleasePending {
		t.Fatalf("updated sandbox = %#v", store.updated)
	}
	if len(store.events) != 2 || store.events[0].Type != "sandbox.runtime_release_failed" || store.events[1].Type != "sandbox.stopped" {
		t.Fatalf("events = %#v, want release failure followed by stopped", store.events)
	}
	if store.events[1].Message != "sandbox stopped; runtime release pending" {
		t.Fatalf("stopped event message = %q", store.events[1].Message)
	}
	if streams.updatedCount != 1 || streams.eventCount != 2 {
		t.Fatalf("stream notifications updated=%d events=%d, want 1/2", streams.updatedCount, streams.eventCount)
	}
}

type projectStopSandboxStore struct {
	session *domain.Sandbox
	updated *domain.Sandbox
	events  []domain.SandboxEvent
}

func (s *projectStopSandboxStore) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	copy := *s.session
	return &copy, nil
}

func (s *projectStopSandboxStore) UpdateSandbox(_ context.Context, session *domain.Sandbox) error {
	copy := *session
	s.updated = &copy
	return nil
}

func (s *projectStopSandboxStore) AddEvent(_ context.Context, _ string, event domain.SandboxEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *projectStopSandboxStore) GetVMState(string) (domain.VMState, error) {
	return domain.VMState{}, nil
}

func (s *projectStopSandboxStore) SaveVMState(string, domain.VMState) error {
	return nil
}

func (s *projectStopSandboxStore) GetProxyState(string) (domain.ProxyState, error) {
	return domain.ProxyState{}, nil
}

type projectStopSandboxDriver struct {
	stopCount    int
	releaseCount int
	releaseErr   error
}

func (*projectStopSandboxDriver) StartSandboxVM(context.Context, *domain.Sandbox) error {
	return nil
}

func (d *projectStopSandboxDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	d.stopCount++
	return nil
}

func (d *projectStopSandboxDriver) ReleaseSandboxRuntime(context.Context, *domain.Sandbox) error {
	d.releaseCount++
	return d.releaseErr
}

type projectStopSandboxStreams struct {
	updatedCount int
	eventCount   int
}

func (s *projectStopSandboxStreams) PublishSandboxUpdated(*domain.SandboxSummary) {
	s.updatedCount++
}

func (s *projectStopSandboxStreams) PublishEventAdded(string, domain.SandboxEvent) {
	s.eventCount++
}

func hasEchoRoute(app *echo.Echo, method string, path string) bool {
	for _, route := range app.Routes() {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
