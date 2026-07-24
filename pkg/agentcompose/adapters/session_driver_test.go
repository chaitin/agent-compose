package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"

	"github.com/samber/do/v2"
)

type fakeSessionRuntime struct {
	info            domain.SandboxVMInfo
	ensureHook      func(*domain.Sandbox)
	ensureStateHook func(domain.VMState)
	ensureErr       error
	removeHook      func(*domain.Sandbox)
	removeErr       error
}

func (r fakeSessionRuntime) EnsureSandbox(_ context.Context, session *domain.Sandbox, vmState domain.VMState, _ domain.ProxyState) (domain.SandboxVMInfo, error) {
	if r.ensureHook != nil {
		r.ensureHook(session)
	}
	if r.ensureStateHook != nil {
		r.ensureStateHook(vmState)
	}
	return r.info, r.ensureErr
}

func (r fakeSessionRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r fakeSessionRuntime) RemoveSandbox(_ context.Context, session *domain.Sandbox, _ domain.VMState) error {
	if r.removeHook != nil {
		r.removeHook(session)
	}
	return r.removeErr
}

func (r fakeSessionRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r fakeSessionRuntime) ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

type fakeStopDeadlineRuntime struct {
	remaining time.Duration
}

func (r *fakeStopDeadlineRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r *fakeStopDeadlineRuntime) StopSandbox(ctx context.Context, _ *domain.Sandbox, _ domain.VMState) (bool, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		r.remaining = time.Until(deadline)
	}
	return false, nil
}

func (r *fakeStopDeadlineRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r *fakeStopDeadlineRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r *fakeStopDeadlineRuntime) ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

type fakeDriverRuntime struct {
	alive bool
}

func (r fakeDriverRuntime) EnsureSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ProxyState) (driverpkg.SandboxVMInfo, error) {
	return driverpkg.SandboxVMInfo{}, nil
}

func (r fakeDriverRuntime) StopSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return false, nil
}

func (r fakeDriverRuntime) RemoveSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error {
	return nil
}

func (r fakeDriverRuntime) Exec(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (r fakeDriverRuntime) ExecStream(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec, driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (r fakeDriverRuntime) IsSandboxAlive(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return r.alive, nil
}

type fakeRuntimeProvider struct {
	runtime SandboxRuntime
	err     error
}

func (p fakeRuntimeProvider) ForDriver(string) (SandboxRuntime, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.runtime, nil
}

func (p fakeRuntimeProvider) ForSession(*domain.Sandbox) (SandboxRuntime, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.runtime, nil
}

func TestSandboxDriverStartSandboxVMSavesRuntimeState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		BoxliteHome:          filepath.Join(root, "boxlite"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	updatedProxyState := domain.ProxyState{
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "agent-compose-sandbox-1",
		HostPort:   39000,
		GuestPort:  8888,
		JupyterURL: "http://127.0.0.1:39000/lab?token=secret",
		Token:      "secret",
	}
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: fakeSessionRuntime{info: domain.SandboxVMInfo{
		BoxID:      "container-1",
		JupyterURL: updatedProxyState.JupyterURL,
		ProxyState: &updatedProxyState,
	}}})

	if err := driver.StartSandboxVM(ctx, session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	savedProxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if savedProxyState.GuestHost != "agent-compose-sandbox-1" || savedProxyState.GuestPort != 8888 {
		t.Fatalf("saved proxy target = %s:%d, want agent-compose-sandbox-1:8888", savedProxyState.GuestHost, savedProxyState.GuestPort)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.BoxID != "container-1" || vmState.BootstrapRef != updatedProxyState.JupyterURL {
		t.Fatalf("vm state = %+v, want box id and bootstrap ref from runtime", vmState)
	}
}

func TestSandboxDriverFreshStartFailureRevokesPreparedAgentToken(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "data.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverBoxlite,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		GuestStateRoot:      "/state",
		GuestHomePath:       "/root",
		RuntimeBaseURL:      "http://agent-compose.test:7410",
		LLMAPIEndpoint:      "https://llm.example.test/v1",
		LLMAPIKey:           "provider-key",
		LLMModel:            "gpt-test",
		LLMAPIProtocol:      "responses",
		SandboxStartTimeout: 2 * time.Second,
	}
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "failed start", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	runner := NewAgentRunner(config, store, configDB, configDB, nil)
	if err := runner.PrepareSandboxAgentEnvironment(ctx, session, execution.AgentConfig{Provider: "codex", Model: "gpt-test"}, nil); err != nil {
		t.Fatalf("PrepareSandboxAgentEnvironment returned error: %v", err)
	}
	rawToken := domain.SandboxEnvMap(session.RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if rawToken == "" {
		t.Fatal("prepared sandbox token is empty")
	}
	startErr := errors.New("runtime start failed")
	driver := NewSandboxDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{ensureErr: startErr}})
	if err := driver.StartSandboxVM(ctx, session); !errors.Is(err, startErr) {
		t.Fatalf("StartSandboxVM error = %v, want %v", err, startErr)
	}
	token, err := configDB.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.RevokedAt.IsZero() {
		t.Fatalf("failed fresh start token remains active: %#v", token)
	}
}

func TestSandboxDriverFailedRunningSandboxCheckKeepsPreparedAgentToken(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "data.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverBoxlite,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		GuestStateRoot:      "/state",
		GuestHomePath:       "/root",
		RuntimeBaseURL:      "http://agent-compose.test:7410",
		LLMAPIEndpoint:      "https://llm.example.test/v1",
		LLMAPIKey:           "provider-key",
		LLMModel:            "gpt-test",
		LLMAPIProtocol:      "responses",
		SandboxStartTimeout: 2 * time.Second,
	}
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "running sandbox", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	runner := NewAgentRunner(config, store, configDB, configDB, nil)
	if err := runner.PrepareSandboxAgentEnvironment(ctx, session, execution.AgentConfig{Provider: "codex", Model: "gpt-test"}, nil); err != nil {
		t.Fatalf("PrepareSandboxAgentEnvironment returned error: %v", err)
	}
	rawToken := domain.SandboxEnvMap(session.RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if rawToken == "" {
		t.Fatal("prepared sandbox token is empty")
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{
		Driver:    driverpkg.RuntimeDriverBoxlite,
		BoxID:     "container-1",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}

	startErr := errors.New("runtime check failed")
	driver := NewSandboxDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{ensureErr: startErr}})
	if err := driver.StartSandboxVM(ctx, session); !errors.Is(err, startErr) {
		t.Fatalf("StartSandboxVM error = %v, want %v", err, startErr)
	}
	token, err := configDB.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if !token.RevokedAt.IsZero() {
		t.Fatalf("running sandbox token was revoked: %#v", token)
	}
}

func TestSandboxDriverPostStartPersistenceFailureKeepsPreparedAgentToken(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "data.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverBoxlite,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		GuestStateRoot:      "/state",
		GuestHomePath:       "/root",
		RuntimeBaseURL:      "http://agent-compose.test:7410",
		LLMAPIEndpoint:      "https://llm.example.test/v1",
		LLMAPIKey:           "provider-key",
		LLMModel:            "gpt-test",
		LLMAPIProtocol:      "responses",
		SandboxStartTimeout: 2 * time.Second,
	}
	configDB, store, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("OpenStores returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "post-start persistence failure", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	runner := NewAgentRunner(config, store, configDB, configDB, nil)
	if err := runner.PrepareSandboxAgentEnvironment(ctx, session, execution.AgentConfig{Provider: "codex", Model: "gpt-test"}, nil); err != nil {
		t.Fatalf("PrepareSandboxAgentEnvironment returned error: %v", err)
	}
	rawToken := domain.SandboxEnvMap(session.RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if rawToken == "" {
		t.Fatal("prepared sandbox token is empty")
	}

	driver := NewSandboxDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		info: domain.SandboxVMInfo{BoxID: "container-1"},
		ensureHook: func(*domain.Sandbox) {
			proxyDir := filepath.Dir(store.ProxyStatePath(session.Summary.ID))
			if err := os.Remove(store.ProxyStatePath(session.Summary.ID)); err != nil {
				t.Fatalf("remove proxy state: %v", err)
			}
			if err := os.Remove(proxyDir); err != nil {
				t.Fatalf("remove proxy directory: %v", err)
			}
			if err := os.WriteFile(proxyDir, []byte("block proxy state persistence"), 0o644); err != nil {
				t.Fatalf("replace proxy directory: %v", err)
			}
		},
	}})
	if err := driver.StartSandboxVM(ctx, session); err == nil {
		t.Fatal("StartSandboxVM succeeded despite proxy state persistence failure")
	}

	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.StartedAt.IsZero() || vmState.BoxID != "container-1" {
		t.Fatalf("runtime start was not persisted before proxy failure: %#v", vmState)
	}
	token, err := configDB.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if !token.RevokedAt.IsZero() {
		t.Fatalf("running sandbox token was revoked after proxy persistence failure: %#v", token)
	}
}

func TestSandboxDriverStopSandboxVMAddsDockerStopContextMargin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		SandboxStartTimeout: 2 * time.Second,
		SandboxStopTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	runtime := &fakeStopDeadlineRuntime{}
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: runtime})

	if err := driver.StopSandboxVM(ctx, session); err != nil {
		t.Fatalf("StopSandboxVM returned error: %v", err)
	}
	if runtime.remaining <= config.SandboxStopTimeout+4*time.Second {
		t.Fatalf("StopSandboxVM context remaining = %s, want docker stop timeout plus API margin", runtime.remaining)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.StoppedAt.IsZero() || vmState.LastError != "" {
		t.Fatalf("vm state after stop = %+v", vmState)
	}
}

func TestSandboxDriverStopPreservesFacadeTokensUntilRemove(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "data.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		SandboxStartTimeout: 2 * time.Second,
		SandboxStopTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	configDB, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverDocker, BoxID: "container-1"}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	rawToken, token, err := llms.NewFacadeToken(session.Summary.ID, "model-1", "provider-1", llms.APIProtocolResponses, "test", "")
	if err != nil {
		t.Fatalf("NewFacadeToken returned error: %v", err)
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}
	removed := false
	runtime := fakeSessionRuntime{removeHook: func(removedSession *domain.Sandbox) {
		removed = removedSession != nil && removedSession.Summary.ID == session.Summary.ID
	}}
	driver := NewSandboxDriver(config, store, configDB, fakeRuntimeProvider{runtime: runtime})

	if err := driver.StopSandboxVM(ctx, session); err != nil {
		t.Fatalf("StopSandboxVM returned error: %v", err)
	}
	storedToken, err := configDB.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken after stop returned error: %v", err)
	}
	if !storedToken.RevokedAt.IsZero() {
		t.Fatalf("facade token revoked during resumable stop: %+v", storedToken)
	}

	if err := driver.RemoveSandboxVM(ctx, session); err != nil {
		t.Fatalf("RemoveSandboxVM returned error: %v", err)
	}
	if !removed {
		t.Fatal("runtime RemoveSandbox was not called")
	}
	storedToken, err = configDB.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("GetLLMFacadeToken after remove returned error: %v", err)
	}
	if storedToken.RevokedAt.IsZero() {
		t.Fatalf("facade token remains active after remove: %+v", storedToken)
	}
}

func TestSandboxDriverResumeReusesRuntimeWithoutRefreshingStartupEnv(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverBoxlite,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		SandboxStartTimeout: 2 * time.Second,
		SandboxStopTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.RuntimeEnvItems = []domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "existing-token"}}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{
		Driver:    driverpkg.RuntimeDriverBoxlite,
		BoxID:     "container-1",
		StoppedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	runtime := fakeSessionRuntime{
		info: domain.SandboxVMInfo{BoxID: "container-1"},
		ensureHook: func(resumed *domain.Sandbox) {
			if got := domain.SandboxEnvMap(resumed.RuntimeEnvItems)["OPENAI_API_KEY"]; got != "existing-token" {
				t.Fatalf("resume runtime env = %#v", resumed.RuntimeEnvItems)
			}
		},
	}
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: runtime})

	if err := driver.StartSandboxVM(ctx, session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.BoxID != "container-1" || !vmState.StoppedAt.IsZero() {
		t.Fatalf("vm state after resume = %+v", vmState)
	}
}

func TestSandboxDriverResumeRecordsAttemptBeforeRuntimeFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"),
		RuntimeDriver: driverpkg.RuntimeDriverBoxlite, BoxliteHome: filepath.Join(root, "boxlite"), DefaultImage: "guest:latest",
		GuestWorkspacePath: "/workspace", SandboxStartTimeout: 2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.CreateSandbox(ctx, "resume failure", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stoppedAt := time.Now().UTC().Add(-48 * time.Hour)
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{
		Driver: driverpkg.RuntimeDriverBoxlite, BoxID: "container-1", StoppedAt: stoppedAt,
	}); err != nil {
		t.Fatal(err)
	}
	startErr := errors.New("runtime partially started")
	var runtimeState domain.VMState
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		ensureErr: startErr, ensureStateHook: func(state domain.VMState) { runtimeState = state },
	}})

	if err := driver.StartSandboxVM(ctx, session); !errors.Is(err, startErr) {
		t.Fatalf("StartSandboxVM error = %v, want %v", err, startErr)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !vmState.StoppedAt.Equal(stoppedAt) || !runtimeState.StoppedAt.Equal(stoppedAt) {
		t.Fatalf("failed resume lost stopped state: persisted=%#v runtime=%#v", vmState, runtimeState)
	}
	if !vmState.StartAttemptedAt.After(stoppedAt) || !runtimeState.StartAttemptedAt.Equal(vmState.StartAttemptedAt) || vmState.LastError != startErr.Error() {
		t.Fatalf("vm state after failed resume = %#v, runtime state = %#v", vmState, runtimeState)
	}
}

func TestSandboxDriverStartSandboxVMUsesPreparedAgentEnvironmentWithoutAddingProviders(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverBoxlite,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		SandboxStartTimeout: 2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.RuntimeEnvItems = []domain.SandboxEnvVar{
		{Name: "OPENAI_API_KEY", Value: "prepared-token"},
		{Name: "OPENAI_BASE_URL", Value: "http://prepared.example/v1"},
	}
	var runtimeEnv map[string]string
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		info: domain.SandboxVMInfo{BoxID: "container-1"},
		ensureHook: func(started *domain.Sandbox) {
			runtimeEnv = domain.SandboxEnvMap(started.RuntimeEnvItems)
		},
	}})

	if err := driver.StartSandboxVM(ctx, session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	if runtimeEnv["OPENAI_API_KEY"] != "prepared-token" || runtimeEnv["OPENAI_BASE_URL"] != "http://prepared.example/v1" {
		t.Fatalf("prepared runtime env = %#v", runtimeEnv)
	}
	if runtimeEnv["ANTHROPIC_API_KEY"] != "" || runtimeEnv["ANTHROPIC_BASE_URL"] != "" {
		t.Fatalf("driver added an unrelated provider environment: %#v", runtimeEnv)
	}
}
