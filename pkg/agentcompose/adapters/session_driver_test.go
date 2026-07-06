package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"

	"github.com/samber/do/v2"
)

type fakeSessionRuntime struct {
	info       domain.SessionVMInfo
	ensureHook func(*domain.Session)
}

func (r fakeSessionRuntime) EnsureSession(_ context.Context, session *domain.Session, _ domain.VMState, _ domain.ProxyState) (domain.SessionVMInfo, error) {
	if r.ensureHook != nil {
		r.ensureHook(session)
	}
	return r.info, nil
}

func (r fakeSessionRuntime) StopSession(context.Context, *domain.Session, domain.VMState) (bool, error) {
	return false, nil
}

func (r fakeSessionRuntime) Exec(context.Context, *domain.Session, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r fakeSessionRuntime) ExecStream(context.Context, *domain.Session, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

type fakeStopDeadlineRuntime struct {
	remaining time.Duration
}

func (r *fakeStopDeadlineRuntime) EnsureSession(context.Context, *domain.Session, domain.VMState, domain.ProxyState) (domain.SessionVMInfo, error) {
	return domain.SessionVMInfo{}, nil
}

func (r *fakeStopDeadlineRuntime) StopSession(ctx context.Context, _ *domain.Session, _ domain.VMState) (bool, error) {
	deadline, ok := ctx.Deadline()
	if ok {
		r.remaining = time.Until(deadline)
	}
	return false, nil
}

func (r *fakeStopDeadlineRuntime) Exec(context.Context, *domain.Session, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r *fakeStopDeadlineRuntime) ExecStream(context.Context, *domain.Session, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

type fakeDriverRuntime struct {
	alive bool
}

func (r fakeDriverRuntime) EnsureSession(context.Context, *driverpkg.Session, driverpkg.VMState, driverpkg.ProxyState) (driverpkg.SessionVMInfo, error) {
	return driverpkg.SessionVMInfo{}, nil
}

func (r fakeDriverRuntime) StopSession(context.Context, *driverpkg.Session, driverpkg.VMState) (bool, error) {
	return false, nil
}

func (r fakeDriverRuntime) Exec(context.Context, *driverpkg.Session, driverpkg.VMState, driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (r fakeDriverRuntime) ExecStream(context.Context, *driverpkg.Session, driverpkg.VMState, driverpkg.ExecSpec, driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (r fakeDriverRuntime) IsSessionAlive(context.Context, *driverpkg.Session, driverpkg.VMState) (bool, error) {
	return r.alive, nil
}

type fakeRuntimeProvider struct {
	runtime BoxRuntime
}

func (p fakeRuntimeProvider) ForDriver(string) (BoxRuntime, error) {
	return p.runtime, nil
}

func (p fakeRuntimeProvider) ForSession(*domain.Session) (BoxRuntime, error) {
	return p.runtime, nil
}

func TestSessionDriverStartSessionVMSavesRuntimeState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		BoxliteHome:          filepath.Join(root, "boxlite"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "adapter session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	updatedProxyState := domain.ProxyState{
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "agent-compose-session-1",
		HostPort:   39000,
		GuestPort:  8888,
		JupyterURL: "http://127.0.0.1:39000/lab?token=secret",
		Token:      "secret",
	}
	driver := NewSessionDriver(config, store, nil, fakeRuntimeProvider{runtime: fakeSessionRuntime{info: domain.SessionVMInfo{
		BoxID:      "container-1",
		JupyterURL: updatedProxyState.JupyterURL,
		ProxyState: &updatedProxyState,
	}}})

	if err := driver.StartSessionVM(ctx, session); err != nil {
		t.Fatalf("StartSessionVM returned error: %v", err)
	}
	savedProxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if savedProxyState.GuestHost != "agent-compose-session-1" || savedProxyState.GuestPort != 8888 {
		t.Fatalf("saved proxy target = %s:%d, want agent-compose-session-1:8888", savedProxyState.GuestHost, savedProxyState.GuestPort)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.BoxID != "container-1" || vmState.BootstrapRef != updatedProxyState.JupyterURL {
		t.Fatalf("vm state = %+v, want box id and bootstrap ref from runtime", vmState)
	}
}

func TestSessionDriverStopSessionVMAddsDockerStopContextMargin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:            root,
		SessionRoot:         filepath.Join(root, "sessions"),
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		DefaultImage:        "guest:latest",
		GuestWorkspacePath:  "/workspace",
		SessionStartTimeout: 2 * time.Second,
		SessionStopTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSession(ctx, "adapter session", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	runtime := &fakeStopDeadlineRuntime{}
	driver := NewSessionDriver(config, store, nil, fakeRuntimeProvider{runtime: runtime})

	if err := driver.StopSessionVM(ctx, session); err != nil {
		t.Fatalf("StopSessionVM returned error: %v", err)
	}
	if runtime.remaining <= config.SessionStopTimeout+4*time.Second {
		t.Fatalf("StopSessionVM context remaining = %s, want docker stop timeout plus API margin", runtime.remaining)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.StoppedAt.IsZero() || vmState.LastError != "" {
		t.Fatalf("vm state after stop = %+v", vmState)
	}
}

func TestSessionDriverStartSessionVMInjectsOpenAIAndAnthropicFacadeEnv(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		DbAddr:               filepath.Join(root, "data.db"),
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverMicrosandbox,
		MicrosandboxHome:     filepath.Join(root, "microsandbox"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
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
	session, err := store.CreateSession(ctx, "adapter session", "", driverpkg.RuntimeDriverMicrosandbox, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.ProviderEnvItems = []domain.SessionEnvVar{
		{Name: "LLM_MODEL", Value: "gpt-test"},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.test"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "anthropic-secret"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-test"},
	}
	updatedProxyState := domain.ProxyState{
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "agent-compose-session-1",
		HostPort:   39000,
		GuestPort:  8888,
		JupyterURL: "http://127.0.0.1:39000/lab?token=secret",
		Token:      "secret",
	}
	var runtimeEnv map[string]string
	driver := NewSessionDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		info: domain.SessionVMInfo{BoxID: "container-1", JupyterURL: updatedProxyState.JupyterURL, ProxyState: &updatedProxyState},
		ensureHook: func(session *domain.Session) {
			runtimeEnv = map[string]string{}
			for _, item := range session.RuntimeEnvItems {
				runtimeEnv[item.Name] = item.Value
			}
		},
	}})

	if err := driver.StartSessionVM(ctx, session); err != nil {
		t.Fatalf("StartSessionVM returned error: %v", err)
	}
	if runtimeEnv["OPENAI_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sessions/"+session.Summary.ID+"/llm/openai/v1" {
		t.Fatalf("OPENAI_BASE_URL = %q", runtimeEnv["OPENAI_BASE_URL"])
	}
	if runtimeEnv["ANTHROPIC_BASE_URL"] != "http://agent-compose.test:7410/api/runtime/sessions/"+session.Summary.ID+"/llm/anthropic" {
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
	if runtimeEnv["AGENT_COMPOSE_SESSION_TOKEN"] == runtimeEnv["ANTHROPIC_AUTH_TOKEN"] {
		t.Fatalf("expected generic session token to remain openai facade token")
	}
}

func TestSessionDriverStartSessionVMIgnoresOptionalClaudeConfigError(t *testing.T) {
	ctx := context.Background()
	originalEnsure := ensureSessionLLMFacadeConfig
	defer func() { ensureSessionLLMFacadeConfig = originalEnsure }()
	ensureSessionLLMFacadeConfig = func(ctx context.Context, config *appconfig.Config, store *configstore.ConfigStore, session *domain.Session, agent, model, source, runID string) (map[string]string, error) {
		switch agent {
		case "codex":
			return map[string]string{
				"AGENT_COMPOSE_SESSION_TOKEN": "openai-token",
				"LLM_API_ENDPOINT":            "http://agent-compose.test:7410/api/runtime/sessions/" + session.Summary.ID + "/llm/openai/v1",
				"LLM_API_KEY":                 "openai-token",
				"LLM_API_PROTOCOL":            "responses",
				"OPENAI_API_KEY":              "openai-token",
				"OPENAI_BASE_URL":             "http://agent-compose.test:7410/api/runtime/sessions/" + session.Summary.ID + "/llm/openai/v1",
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
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverMicrosandbox,
		MicrosandboxHome:     filepath.Join(root, "microsandbox"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SessionStartTimeout:  2 * time.Second,
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
	session, err := store.CreateSession(ctx, "adapter session", "", driverpkg.RuntimeDriverMicrosandbox, "guest:latest", "", domain.SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	updatedProxyState := domain.ProxyState{
		ProxyPath:  session.Summary.ProxyPath,
		GuestHost:  "agent-compose-session-1",
		HostPort:   39000,
		GuestPort:  8888,
		JupyterURL: "http://127.0.0.1:39000/lab?token=secret",
		Token:      "secret",
	}
	var runtimeEnv map[string]string
	driver := NewSessionDriver(config, store, configDB, fakeRuntimeProvider{runtime: fakeSessionRuntime{
		info: domain.SessionVMInfo{BoxID: "container-1", JupyterURL: updatedProxyState.JupyterURL, ProxyState: &updatedProxyState},
		ensureHook: func(session *domain.Session) {
			runtimeEnv = map[string]string{}
			for _, item := range session.RuntimeEnvItems {
				runtimeEnv[item.Name] = item.Value
			}
		},
	}})

	if err := driver.StartSessionVM(ctx, session); err != nil {
		t.Fatalf("StartSessionVM returned error: %v", err)
	}
	if runtimeEnv["OPENAI_BASE_URL"] == "" || runtimeEnv["OPENAI_API_KEY"] == "" {
		t.Fatalf("missing openai facade env: %#v", runtimeEnv)
	}
	if runtimeEnv["ANTHROPIC_BASE_URL"] != "" || runtimeEnv["ANTHROPIC_AUTH_TOKEN"] != "" || runtimeEnv["ANTHROPIC_API_KEY"] != "" {
		t.Fatalf("expected optional claude env to be skipped, got %#v", runtimeEnv)
	}
}
