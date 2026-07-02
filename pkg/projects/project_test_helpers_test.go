package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agent-compose/pkg/bus"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	executorpkg "agent-compose/pkg/executor"
	imagespkg "agent-compose/pkg/images"
	llmpkg "agent-compose/pkg/llm"
	loaderspkg "agent-compose/pkg/loaders"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage"
	"agent-compose/pkg/workspaces"
)

const (
	agentResultPrefix             = "__AGENT_RESULT__"
	commandResultPrefix           = "__COMMAND_RESULT__"
	capProxyTargetEnvName         = capabilities.CapProxyTargetEnvName
	capabilitySessionTokenEnvName = capabilities.CapabilitySessionTokenEnvName
	llmProviderScopeSessionEnv    = llmpkg.ProviderScopeSessionEnv
)

type BoxRuntime = runtimes.BoxRuntime
type RuntimeProvider = runtimes.RuntimeProvider
type SessionVMInfo = runtimes.SessionVMInfo
type VMState = model.VMState
type ProxyState = model.ProxyState
type ExecSpec = model.ExecSpec
type ExecResult = model.ExecResult
type ExecStreamWriter = model.ExecStreamWriter
type RuntimeCommandResult = model.RuntimeCommandResult

type LoaderManager = loaderspkg.LoaderManager
type QJSLoaderEngine = loaderspkg.QJSLoaderEngine
type LoaderEvent = model.LoaderEvent

type ImageListRequest = imagespkg.ImageListRequest
type ImageListResult = imagespkg.ImageListResult
type ImagePullResult = imagespkg.ImagePullResult
type ImageInspectResult = imagespkg.ImageInspectResult
type ImageRemoveRequest = imagespkg.ImageRemoveRequest
type ImageRemoveResult = imagespkg.ImageRemoveResult

func newTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	root := t.TempDir()
	return mustTestConfigStore(t, &appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
}

func mustTestStore(t testing.TB, config *appconfig.Config) *Store {
	t.Helper()
	store, err := storage.NewStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewStoreFromConfig returned error: %v", err)
	}
	return store
}

func mustTestConfigStore(t testing.TB, config *appconfig.Config) *ConfigStore {
	t.Helper()
	store, err := storage.NewConfigStoreFromConfig(config)
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	return store
}

func NewLoaderBusWithBuffer(size int) *bus.LoaderBus {
	return bus.NewLoaderBusWithBuffer(size)
}

func newTestLoaderManager(t testing.TB, deps loaderspkg.ManagerDeps) *LoaderManager {
	t.Helper()
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	if deps.Config == nil {
		root := t.TempDir()
		deps.Config = &appconfig.Config{
			DataRoot:    filepath.Join(root, "data"),
			SessionRoot: filepath.Join(root, "sessions"),
		}
	}
	if deps.ConfigDB == nil {
		tt, ok := t.(*testing.T)
		if !ok {
			t.Fatalf("newTestLoaderManager requires *testing.T when ConfigDB is omitted")
		}
		deps.ConfigDB = newTestConfigStore(tt)
	}
	if deps.Bus == nil {
		deps.Bus = NewLoaderBusWithBuffer(16)
	}
	if deps.Engine == nil {
		deps.Engine = &QJSLoaderEngine{}
	}
	if deps.Images == nil {
		deps.Images = &fakeImageBackend{}
	}
	if deps.Executor == nil {
		deps.Executor = loaderspkg.NewExecutor(deps.Config, deps.Store, deps.ConfigDB, nil, nil)
	}
	manager, err := loaderspkg.NewManager(deps)
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}
	return manager
}

type fakeImageBackend struct {
	listImages   func(context.Context, ImageListRequest) (ImageListResult, error)
	pullImage    func(context.Context, ImagePullRequest) (ImagePullResult, error)
	inspectImage func(context.Context, ImageInspectRequest) (ImageInspectResult, error)
	removeImage  func(context.Context, ImageRemoveRequest) (ImageRemoveResult, error)
}

func (b *fakeImageBackend) ListImages(ctx context.Context, req ImageListRequest) (ImageListResult, error) {
	if b != nil && b.listImages != nil {
		return b.listImages(ctx, req)
	}
	return ImageListResult{}, nil
}

func (b *fakeImageBackend) PullImage(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
	if b != nil && b.pullImage != nil {
		return b.pullImage(ctx, req)
	}
	return ImagePullResult{}, nil
}

func (b *fakeImageBackend) InspectImage(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
	if b != nil && b.inspectImage != nil {
		return b.inspectImage(ctx, req)
	}
	return ImageInspectResult{}, nil
}

func (b *fakeImageBackend) RemoveImage(ctx context.Context, req ImageRemoveRequest) (ImageRemoveResult, error) {
	if b != nil && b.removeImage != nil {
		return b.removeImage(ctx, req)
	}
	return ImageRemoveResult{}, nil
}

type fakeSessionDriver struct {
	startCalls []string
	stopCalls  []string
	startHook  func(context.Context, *Session) error
	stopHook   func(context.Context, *Session) error
}

func (d *fakeSessionDriver) StartSessionVM(ctx context.Context, session *Session) error {
	d.startCalls = append(d.startCalls, session.Summary.ID)
	if d.startHook != nil {
		return d.startHook(ctx, session)
	}
	return nil
}

func (d *fakeSessionDriver) StopSessionVM(ctx context.Context, session *Session) error {
	d.stopCalls = append(d.stopCalls, session.Summary.ID)
	if d.stopHook != nil {
		return d.stopHook(ctx, session)
	}
	return nil
}

type fixedGatewaySource struct {
	settings storage.CapabilityGatewaySettings
}

func (f fixedGatewaySource) GetCapabilityGateway(context.Context) (storage.CapabilityGatewaySettings, error) {
	return f.settings, nil
}

func newTestCapabilityProvider(addr, proxyTarget string) CapabilityProvider {
	return capabilities.NewProvider(fixedGatewaySource{settings: storage.CapabilityGatewaySettings{Addr: addr}}, proxyTarget)
}

func sessionCapabilityGuidePath(session *Session) string {
	return capabilities.SessionGuidePath(session)
}

func llmProviderKeyName(name string) bool {
	return llmpkg.ProviderKeyName(name)
}

func fileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	return workspaces.FileWorkspaceContentRoot(config, workspace)
}

func sessionHasTag(session *Session, name, value string) bool {
	if session == nil {
		return false
	}
	for _, tag := range session.Summary.Tags {
		if tag.Name == name && tag.Value == value {
			return true
		}
	}
	return false
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("file %s = %q, want %q", path, string(data), want)
	}
}

func newProjectTestStreamBroker(t testing.TB) *sessions.SessionStreamBroker {
	t.Helper()
	streams, err := sessions.NewSessionStreamBroker(nil)
	if err != nil {
		t.Fatalf("NewSessionStreamBroker returned error: %v", err)
	}
	return streams
}

func newProjectTestExecutor(config *appconfig.Config, store *Store, configDB *ConfigStore, provider RuntimeProvider, streams *sessions.SessionStreamBroker) *Executor {
	return executorpkg.New(config, store, configDB, provider, streams, llmpkg.EnsureSessionLLMFacadeConfig)
}

var projectTestRuntimeRegistry = struct {
	sync.Mutex
	items map[*Service]fixedRuntimeProvider
}{items: map[*Service]fixedRuntimeProvider{}}

func registerProjectTestRuntime(service *Service, runtime *fakeLoaderAgentRuntime) {
	projectTestRuntimeRegistry.Lock()
	defer projectTestRuntimeRegistry.Unlock()
	projectTestRuntimeRegistry.items[service] = fixedRuntimeProvider{runtime: runtime}
}

func projectTestRuntimeProvider(t testing.TB, service *Service) fixedRuntimeProvider {
	t.Helper()
	projectTestRuntimeRegistry.Lock()
	defer projectTestRuntimeRegistry.Unlock()
	provider, ok := projectTestRuntimeRegistry.items[service]
	if !ok {
		t.Fatalf("runtime provider for service %p is not registered", service)
	}
	return provider
}

func projectTestFakeRuntime(t testing.TB, service *Service) *fakeLoaderAgentRuntime {
	t.Helper()
	runtime, ok := projectTestRuntimeProvider(t, service).runtime.(*fakeLoaderAgentRuntime)
	if !ok {
		t.Fatalf("fixed runtime = %T, want *fakeLoaderAgentRuntime", projectTestRuntimeProvider(t, service).runtime)
	}
	return runtime
}

type fixedRuntimeProvider struct {
	runtime BoxRuntime
}

func (p fixedRuntimeProvider) ForDriver(string) (BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

func (p fixedRuntimeProvider) ForSession(*Session) (BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

type fakeLoaderAgentRuntime struct {
	execCalls              int
	providers              []string
	agentSpecs             []ExecSpec
	agentDeadlineDurations []time.Duration
	agentStdout            string
	agentStderr            string
	agentOutput            string
	agentNoPayload         bool
	agentWaitForContext    bool
	commandSpecs           []ExecSpec
	commands               []ExecSpec
	commandExitCode        int
	commandStdout          string
	commandStderr          string
	commandOutput          string
	commandBlock           chan struct{}
	commandNoPayload       bool
	commandStreamHook      func()
	agentExitCode          int
}

func (r *fakeLoaderAgentRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, nil
}

func (r *fakeLoaderAgentRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return true, nil
}

func (r *fakeLoaderAgentRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("unexpected Exec call")
}

func (r *fakeLoaderAgentRuntime) ExecStream(ctx context.Context, session *Session, _ VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	r.execCalls++
	if isLoaderCommandExecSpec(spec) {
		r.commandSpecs = append(r.commandSpecs, spec)
		r.commands = append(r.commands, spec)
		stdout := firstNonEmpty(r.commandStdout, "command stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		cellID := "cell-fake"
		if session != nil {
			hostCellDir := filepath.Join(hostSessionDir(session), "state", "cells", cellID)
			_ = os.MkdirAll(hostCellDir, 0o755)
		}
		result := RuntimeCommandResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}
		payload, _ := json.Marshal(result)
		if stream != nil {
			stream(ExecChunk{Text: stdout})
			if stderr != "" {
				stream(ExecChunk{Text: stderr, IsStderr: true})
			}
			if r.commandStreamHook != nil {
				r.commandStreamHook()
			}
		}
		if r.commandBlock != nil {
			<-r.commandBlock
		}
		if r.commandNoPayload {
			return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}, nil
		}
		payloadText := commandResultPrefix + string(payload)
		return ExecResult{Stdout: payloadText, Stderr: "", Output: output + payloadText, ExitCode: r.commandExitCode, Success: r.commandExitCode == 0}, nil
	}
	if spec.Command == "bash" || spec.Command == "node" || spec.Command == "python3" {
		stdout := firstNonEmpty(r.commandStdout, "cell stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		if stream != nil {
			stream(ExecChunk{Text: stdout})
			if stderr != "" {
				stream(ExecChunk{Text: stderr, IsStderr: true})
			}
		}
		exitCode := r.commandExitCode
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: exitCode, Success: exitCode == 0}, nil
	}

	if deadline, ok := ctx.Deadline(); ok {
		r.agentDeadlineDurations = append(r.agentDeadlineDurations, time.Until(deadline))
	}
	provider := providerFromExecSpec(spec)
	r.providers = append(r.providers, provider)
	r.agentSpecs = append(r.agentSpecs, spec)
	if stream != nil {
		stream(ExecChunk{Text: "loader agent transcript\n", IsStderr: true})
	}
	exitCode := r.agentExitCode
	if r.agentWaitForContext {
		<-ctx.Done()
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: firstNonZeroInt(exitCode, 1), Success: false}, ctx.Err()
	}
	if r.agentNoPayload {
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return ExecResult{Stdout: stdout, Stderr: stderr, Output: output, ExitCode: exitCode, Success: exitCode == 0}, nil
	}
	payload := agentResultPrefix + fmt.Sprintf(`{"provider":%q,"agent":%q,"sessionId":"agent-runtime-session","stopReason":"completed","finalText":"loader agent transcript","transcript":"loader agent transcript","success":%t,"exitCode":%d}`, provider, provider, exitCode == 0, exitCode)
	return ExecResult{Stdout: payload, Stderr: "loader agent transcript\n", Output: "loader agent transcript\n" + payload, ExitCode: exitCode, Success: exitCode == 0}, nil
}

func isLoaderCommandExecSpec(spec ExecSpec) bool {
	if spec.Command != "sh" {
		return false
	}
	return strings.Contains(strings.Join(spec.Args, " "), "agent-compose-runtime exec")
}

func providerFromExecSpec(spec ExecSpec) string {
	provider := "codex"
	for index, arg := range spec.Args {
		if arg == "--provider" && index+1 < len(spec.Args) {
			return strings.Trim(spec.Args[index+1], "'\"")
		}
		marker := "--provider "
		position := strings.Index(arg, marker)
		if position < 0 {
			continue
		}
		remainder := strings.TrimSpace(arg[position+len(marker):])
		if remainder == "" {
			continue
		}
		return strings.Trim(strings.Fields(remainder)[0], "'\"")
	}
	return provider
}
