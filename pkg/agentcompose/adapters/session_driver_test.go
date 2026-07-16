package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
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
	do.ProvideValue(di, config)
	configDB, err := configstore.NewConfigStore(di)
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
	originalEnsure := ensureSandboxLLMFacadeConfig
	defer func() { ensureSandboxLLMFacadeConfig = originalEnsure }()
	ensureCalls := 0
	ensureSandboxLLMFacadeConfig = func(context.Context, *appconfig.Config, runtimefacade.FacadeStore, *domain.Sandbox, string, string, string, string) (map[string]string, error) {
		ensureCalls++
		return map[string]string{"OPENAI_API_KEY": "new-token"}, nil
	}
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
			if len(resumed.RuntimeEnvItems) != 0 {
				t.Fatalf("resume injected replacement startup env: %#v", resumed.RuntimeEnvItems)
			}
		},
	}
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: runtime})

	if err := driver.StartSandboxVM(ctx, session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	if ensureCalls != 0 {
		t.Fatalf("startup facade refresh calls during resume = %d, want 0", ensureCalls)
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

func TestSandboxDriverStartSandboxVMInjectsOpenAIAndAnthropicFacadeEnv(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		DbAddr:               filepath.Join(root, "data.db"),
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverMicrosandbox,
		MicrosandboxHome:     filepath.Join(root, "microsandbox"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
		RuntimeBaseURL:       "http://agent-compose.test:7410",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	di := do.New()
	do.ProvideValue(di, config)
	configDB, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverMicrosandbox, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.ProviderEnvItems = []domain.SandboxEnvVar{
		{Name: "LLM_MODEL", Value: "gpt-test"},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.test"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "anthropic-secret"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-test"},
	}
	updatedProxyState := domain.ProxyState{
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "agent-compose-sandbox-1",
		HostPort:   39000,
		GuestPort:  8888,
		JupyterURL: "http://127.0.0.1:39000/lab?token=secret",
		Token:      "secret",
	}
	var runtimeEnv map[string]string
	driver := NewSandboxDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		info: domain.SandboxVMInfo{BoxID: "container-1", JupyterURL: updatedProxyState.JupyterURL, ProxyState: &updatedProxyState},
		ensureHook: func(session *domain.Sandbox) {
			runtimeEnv = map[string]string{}
			for _, item := range session.RuntimeEnvItems {
				runtimeEnv[item.Name] = item.Value
			}
		},
	}})

	if err := driver.StartSandboxVM(ctx, session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	if runtimeEnv["OPENAI_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sandboxes/"+session.Summary.ID+"/llm/openai/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q", runtimeEnv["OPENAI_BASE_URL"])
	}
	if runtimeEnv["ANTHROPIC_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sandboxes/"+session.Summary.ID+"/llm/anthropic" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q", runtimeEnv["ANTHROPIC_BASE_URL"])
	}
	if runtimeEnv["OPENAI_API_KEY"] == "" {
		t.Fatalf("OPENAI_API_KEY is empty")
	}
	if runtimeEnv["LLM_API_PROTOCOL"] != "responses" {
		t.Fatalf("LLM_API_PROTOCOL = %q", runtimeEnv["LLM_API_PROTOCOL"])
	}
	if runtimeEnv["LLM_API_ENDPOINT"] != runtimeEnv["OPENAI_BASE_URL"] {
		t.Fatalf("LLM_API_ENDPOINT = %q, want openai base %q", runtimeEnv["LLM_API_ENDPOINT"], runtimeEnv["OPENAI_BASE_URL"])
	}
	if runtimeEnv["ANTHROPIC_AUTH_TOKEN"] == "" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN is empty")
	}
	if runtimeEnv["ANTHROPIC_AUTH_TOKEN"] != runtimeEnv["ANTHROPIC_API_KEY"] {
		t.Fatalf("anthropic token mismatch auth=%q api=%q", runtimeEnv["ANTHROPIC_AUTH_TOKEN"], runtimeEnv["ANTHROPIC_API_KEY"])
	}
	if runtimeEnv["ANTHROPIC_AUTH_TOKEN"] == "anthropic-secret" {
		t.Fatalf("expected managed anthropic facade token, got raw provider token")
	}
	if runtimeEnv["AGENT_COMPOSE_SANDBOX_TOKEN"] == runtimeEnv["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatalf("expected generic sandbox token to remain openai facade token")
	}
	if runtimeEnv["AGENT_COMPOSE_SESSION_TOKEN"] != "" {
		t.Fatalf("AGENT_COMPOSE_SESSION_TOKEN should not be injected")
	}
}

func TestSandboxDriverStartSandboxVMIgnoresOptionalClaudeConfigError(t *testing.T) {
	ctx := context.Background()
	originalEnsure := ensureSandboxLLMFacadeConfig
	defer func() { ensureSandboxLLMFacadeConfig = originalEnsure }()
	ensureSandboxLLMFacadeConfig = func(ctx context.Context, config *appconfig.Config, store runtimefacade.FacadeStore, session *domain.Sandbox, agent, model, source, runID string) (map[string]string, error) {
		switch agent {
		case "codex":
			return map[string]string{
				"AGENT_COMPOSE_SANDBOX_TOKEN": "openai-token",
				"LLM_API_ENDPOINT":            "http://agent-compose.test:7410/api/runtime/sandboxes/" + session.Summary.ID + "/llm/openai/v1",
				"LLM_API_KEY":                 "openai-token",
				"LLM_API_PROTOCOL":            "responses",
				"OPENAI_API_KEY":              "openai-token",
				"OPENAI_BASE_URL":             "http://agent-compose.test:7410/api/runtime/sandboxes/" + session.Summary.ID + "/llm/openai/v1",
			}, nil
		case "claude":
			return nil, domain.ClassifyError(domain.ErrRequired, "anthropic provider is required", nil)
		default:
			return nil, errors.New("unexpected agent")
		}
	}
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		DbAddr:               filepath.Join(root, "data.db"),
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverMicrosandbox,
		MicrosandboxHome:     filepath.Join(root, "microsandbox"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
		RuntimeBaseURL:       "http://agent-compose.test:7410",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	di := do.New()
	do.ProvideValue(di, config)
	configDB, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "adapter session", "", driverpkg.RuntimeDriverMicrosandbox, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
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
	var runtimeEnv map[string]string
	driver := NewSandboxDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		info: domain.SandboxVMInfo{BoxID: "container-1", JupyterURL: updatedProxyState.JupyterURL, ProxyState: &updatedProxyState},
		ensureHook: func(session *domain.Sandbox) {
			runtimeEnv = map[string]string{}
			for _, item := range session.RuntimeEnvItems {
				runtimeEnv[item.Name] = item.Value
			}
		},
	}})

	if err := driver.StartSandboxVM(ctx, session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	if runtimeEnv["OPENAI_BASE_URL"] == "" || runtimeEnv["OPENAI_API_KEY"] == "" {
		t.Fatalf("missing openai facade env: %#v", runtimeEnv)
	}
	if runtimeEnv["ANTHROPIC_BASE_URL"] != "" || runtimeEnv["ANTHROPIC_AUTH_TOKEN"] != "" || runtimeEnv["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("expected optional claude env to be skipped, got %#v", runtimeEnv)
	}
}
