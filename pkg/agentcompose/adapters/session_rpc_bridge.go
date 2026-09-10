package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/dashboard"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type SandboxRPCBridge struct {
	config           *appconfig.Config
	store            *sandboxstore.Store
	configDB         *configstore.ConfigStore
	workspaceEnsurer workspaces.WorkspaceEnsurer
	driver           sandboxes.SandboxDriver
	runtimes         RuntimeProvider
	bus              *schedulers.Bus
	streams          *sandboxes.StreamBroker
	cap              capabilities.Provider
	capTokens        *CapabilitySandboxResolver
	dashboard        *dashboard.Hub
	agentExecutor    *AgentExecutor
	lifecycleLocks   *sandboxes.LifecycleLocks
}

// SandboxRPCBridgeDeps bundles NewSandboxRPCBridge's required dependencies.
type SandboxRPCBridgeDeps struct {
	Config           *appconfig.Config
	Store            *sandboxstore.Store
	ConfigDB         *configstore.ConfigStore
	WorkspaceEnsurer workspaces.WorkspaceEnsurer
	Driver           sandboxes.SandboxDriver
	Runtimes         RuntimeProvider
	Bus              *schedulers.Bus
	Streams          *sandboxes.StreamBroker
	Cap              capabilities.Provider
	CapTokens        *CapabilitySandboxResolver
	Dashboard        *dashboard.Hub
	AgentExecutor    *AgentExecutor
}

func NewSandboxRPCBridge(deps SandboxRPCBridgeDeps, locks ...*sandboxes.LifecycleLocks) *SandboxRPCBridge {
	bridge := &SandboxRPCBridge{
		config:           deps.Config,
		store:            deps.Store,
		configDB:         deps.ConfigDB,
		workspaceEnsurer: deps.WorkspaceEnsurer,
		driver:           deps.Driver,
		runtimes:         deps.Runtimes,
		bus:              deps.Bus,
		streams:          deps.Streams,
		cap:              deps.Cap,
		capTokens:        deps.CapTokens,
		dashboard:        deps.Dashboard,
		agentExecutor:    deps.AgentExecutor,
	}
	if len(locks) > 0 {
		bridge.lifecycleLocks = locks[0]
	}
	return bridge
}

func (b *SandboxRPCBridge) SubscribeSandbox(sandboxID string) (<-chan sandboxes.WatchEvent, func()) {
	return b.streams.Subscribe(sandboxID)
}

func (b *SandboxRPCBridge) CallJSON(ctx context.Context, method, requestJSON string) (string, error) {
	return b.CallJSONWithSource(ctx, method, requestJSON, domain.SandboxTypeScript)
}

func (b *SandboxRPCBridge) CallJSONWithSource(ctx context.Context, method, requestJSON, source string) (string, error) {
	method = strings.TrimSpace(method)
	switch method {
	case "CreateSandbox":
		var request sandboxRPCCreateRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		loaded, err := b.createSandboxWithAgent(ctx, request, source, schedulers.SandboxCreationContextFromContext(ctx))
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Sandbox: sandboxRPCDetailFromDomain(loaded)})
	case "ResumeSandbox":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, err := b.resumeSandbox(ctx, request.ID(), source)
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Sandbox: sandboxRPCDetailFromDomain(sandbox)})
	case "StopSandbox":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, err := b.stopSandbox(ctx, request.ID(), source)
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Sandbox: sandboxRPCDetailFromDomain(sandbox)})
	case "GetSandbox":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, err := b.getSandbox(ctx, request.ID())
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Sandbox: sandboxRPCDetailFromDomain(sandbox)})
	case "ListSandboxes":
		var request sandboxRPCListRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		options, err := request.Options()
		if err != nil {
			return "", err
		}
		result, err := b.listSandboxes(ctx, options)
		if err != nil {
			return "", err
		}
		response := sandboxRPCListResponse{TotalCount: uint32(result.TotalCount), HasMore: result.HasMore, NextOffset: uint32(result.NextOffset)}
		for _, sandbox := range result.Sandboxes {
			response.Sandboxes = append(response.Sandboxes, sandboxRPCSummaryFromDomain(&sandbox.Summary))
		}
		return encodeSandboxRPCJSON(response)
	case "GetSandboxProxy":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, proxy, err := b.getSandboxProxy(ctx, request.ID())
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCProxyResponse{SandboxID: sandbox.Summary.ID, ProxyPath: proxy.ProxyPath, NotebookURL: proxy.NotebookURL, Driver: sandbox.Summary.Driver, VMStatus: sandbox.Summary.VMStatus})
	default:
		return "", fmt.Errorf("unsupported sandbox rpc %q", method)
	}
}

func (b *SandboxRPCBridge) publishSchedulerTopic(topic string, payload map[string]any) {
	if b == nil || b.bus == nil {
		return
	}
	b.bus.Publish(domain.SchedulerTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (b *SandboxRPCBridge) createSandbox(ctx context.Context, req sandboxRPCCreateRequest, source string) (*domain.Sandbox, error) {
	return b.createSandboxWithAgent(ctx, req, source, schedulers.SandboxCreationContext{})
}

// resolvedSandboxCreateAgentConfig is the agent config, tags, env items, and
// workspace/driver/image resolveSandboxCreateAgentConfig derives before
// createSandboxWithAgent creates the sandbox.
type resolvedSandboxCreateAgentConfig struct {
	AgentConfig       execution.AgentConfig
	AgentDefinition   *domain.AgentDefinition
	ProviderEnvItems  []domain.SandboxEnvVar
	EnvItems          []domain.SandboxEnvVar
	Tags              []domain.SandboxTag
	WorkspaceSnapshot *domain.SandboxWorkspace
	WorkspaceID       string
	Driver            string
	GuestImage        string
}

func (b *SandboxRPCBridge) resolveSandboxCreateAgentConfig(ctx context.Context, req sandboxRPCCreateRequest, creation schedulers.SandboxCreationContext) (resolvedSandboxCreateAgentConfig, error) {
	agentConfig := execution.AgentConfig{Provider: domain.NormalizeAgentKind(creation.Provider)}
	var agentDefinition *domain.AgentDefinition
	if agentID := strings.TrimSpace(creation.AgentDefinitionID); agentID != "" {
		definition, err := b.configDB.GetAgentDefinition(ctx, agentID)
		if err != nil {
			return resolvedSandboxCreateAgentConfig{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("resolve sandbox agent definition %s: %w", agentID, err))
		}
		if !definition.Enabled {
			return resolvedSandboxCreateAgentConfig{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox agent definition %s is disabled", agentID))
		}
		agentDefinition = &definition
		agentConfig = execution.AgentConfigFromDefinition(definition, domain.DefaultAgentProvider)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = domain.DefaultAgentProvider
	}
	tags := sandboxTagsWithoutAgentIdentity(req.Tags)
	tags = append(tags, domain.SandboxTag{Name: domain.AgentSandboxTagProvider, Value: agentConfig.Provider})
	providerEnvItems := append([]domain.SandboxEnvVar(nil), req.EnvItems...)
	globalEnvItems, err := b.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return resolvedSandboxCreateAgentConfig{}, connect.NewError(connect.CodeInternal, err)
	}
	if agentDefinition != nil {
		providerEnvItems = domain.MergeEnvItems(agentDefinition.EnvItems, providerEnvItems)
		tags = append(tags,
			domain.SandboxTag{Name: domain.AgentSandboxTagSource, Value: domain.AgentSandboxTagSourceVal},
			domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: agentDefinition.ID},
			domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: agentDefinition.Name},
		)
	}
	envItems := domain.MergeEnvItems(globalEnvItems, providerEnvItems)
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	capabilityVars, capabilityTags := capabilities.BuildGatewaySandboxVars(capabilities.ProxyTarget(b.cap), req.CapsetIDs)
	envItems = domain.MergeEnvItems(envItems, capabilityVars)
	tags = append(tags, capabilityTags...)

	var workspaceSnapshot *domain.SandboxWorkspace
	workspaceID := strings.TrimSpace(req.WorkspaceID)
	if workspaceID != "" {
		workspaceConfig, err := b.configDB.GetWorkspaceConfig(ctx, workspaceID)
		if err != nil {
			return resolvedSandboxCreateAgentConfig{}, connect.NewError(connect.CodeInvalidArgument, err)
		}
		workspaceSnapshot = toSandboxWorkspaceSnapshot(workspaceConfig)
	}

	driver, err := driverpkg.ResolveSandboxRuntimeDriver(req.Driver, b.config.RuntimeDriver)
	if err != nil {
		return resolvedSandboxCreateAgentConfig{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := driverpkg.ValidateCompiledRuntimeDriver(driver); err != nil {
		return resolvedSandboxCreateAgentConfig{}, api.ConnectErrorForDomain(classifyRuntimeProviderError(err))
	}
	guestImage := driverpkg.ResolveSandboxGuestImage(req.GuestImage, driverpkg.DefaultGuestImageForDriver(b.config, driver))
	return resolvedSandboxCreateAgentConfig{
		AgentConfig:       agentConfig,
		AgentDefinition:   agentDefinition,
		ProviderEnvItems:  providerEnvItems,
		EnvItems:          envItems,
		Tags:              tags,
		WorkspaceSnapshot: workspaceSnapshot,
		WorkspaceID:       workspaceID,
		Driver:            driver,
		GuestImage:        guestImage,
	}, nil
}

func (b *SandboxRPCBridge) createSandboxWithAgent(ctx context.Context, req sandboxRPCCreateRequest, source string, creation schedulers.SandboxCreationContext) (*domain.Sandbox, error) {
	cfg, err := b.resolveSandboxCreateAgentConfig(ctx, req, creation)
	if err != nil {
		return nil, err
	}
	agentConfig, agentDefinition, providerEnvItems := cfg.AgentConfig, cfg.AgentDefinition, cfg.ProviderEnvItems
	session, err := b.store.CreateSandboxWithOptions(ctx, req.Title, req.BaseWorkspace, cfg.Driver, cfg.GuestImage, cfg.WorkspaceID, source, cfg.WorkspaceSnapshot, cfg.EnvItems, cfg.Tags, sandboxstore.CreateSandboxOptions{
		StoppedRuntimePolicy: stoppedRuntimePolicyFromAgentDefinition(agentDefinition),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	llms.SetSandboxProviderEnvItems(session, providerEnvItems)
	if err := b.workspaceEnsurer.Ensure(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSandbox(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runs.WriteCapabilityGuide(ctx, runs.CapabilityGuideDeps{
		Provider:       b.cap,
		Store:          b.store,
		Streams:        b.streams,
		Config:         b.config,
		WriteGuestFile: b.agentExecutor.GuestFileWriterFor(session),
	}, session, req.CapsetIDs)
	if b.agentExecutor == nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSandbox(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("agent executor is required"))
	}
	if err := b.agentExecutor.PrepareSandboxAgentEnvironment(ctx, session, agentConfig, agentDefinition); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSandbox(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := b.driver.StartSandboxVM(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSandbox(ctx, session)
		return nil, api.ConnectErrorForDomain(err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := b.store.UpdateSandbox(ctx, session); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	b.streams.PublishSandboxUpdated(&session.Summary)
	if b.dashboard != nil {
		b.dashboard.Notify("sandbox_updated")
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "sandbox.created",
		Level:     "info",
		Message:   fmt.Sprintf("sandbox started with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage),
		CreatedAt: time.Now().UTC(),
	}
	_ = b.store.AddEvent(ctx, session.Summary.ID, event)
	b.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := b.store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	domain.RestoreSandboxTransientFields(loaded, session)
	b.indexCapabilitySandbox(loaded)
	b.publishSchedulerTopic("agent-compose.session.created", schedulers.SessionTopicPayload(loaded, source))
	return loaded, nil
}

func sandboxTagsWithoutAgentIdentity(tags []domain.SandboxTag) []domain.SandboxTag {
	filtered := make([]domain.SandboxTag, 0, len(tags))
	for _, tag := range tags {
		switch strings.TrimSpace(tag.Name) {
		case domain.AgentSandboxTagID, domain.AgentSandboxTagName, domain.AgentSandboxTagProvider:
			continue
		default:
			filtered = append(filtered, tag)
		}
	}
	return filtered
}

func (b *SandboxRPCBridge) ResumeSandbox(ctx context.Context, sandboxID string) (*domain.Sandbox, error) {
	return b.resumeSandbox(ctx, sandboxID, domain.SandboxTypeManual)
}

func (b *SandboxRPCBridge) resumeSandbox(ctx context.Context, sandboxID, source string) (*domain.Sandbox, error) {
	session, err := b.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	loaded, err := b.sessionLifecycle().ResumeLoaded(ctx, session, capabilities.SandboxCapsets(session))
	if err != nil {
		return nil, api.ConnectErrorForDomain(err)
	}
	b.indexCapabilitySandbox(loaded)
	b.publishSchedulerTopic("agent-compose.session.resumed", schedulers.SessionTopicPayload(loaded, source))
	return loaded, nil
}

func (b *SandboxRPCBridge) ReconcileRuntimeState(ctx context.Context, session *domain.Sandbox) (*domain.Sandbox, error) {
	return b.sessionLifecycle().ReconcileRuntimeState(ctx, session)
}

func (b *SandboxRPCBridge) RecoverStoppedRuntimeReleases(ctx context.Context) []string {
	return b.sessionLifecycle().RecoverStoppedRuntimeReleases(ctx)
}

func (b *SandboxRPCBridge) StopSandbox(ctx context.Context, sandboxID string) (*domain.Sandbox, error) {
	outcome, err := b.stopSandboxWithOptions(ctx, sandboxID, domain.SandboxTypeManual, sandboxes.StopOptions{Mode: sandboxes.StopModeForce})
	return outcome.Sandbox, err
}

func (b *SandboxRPCBridge) StopSandboxWithOptions(ctx context.Context, sandboxID string, options sandboxes.StopOptions) (sandboxes.StopOutcome, error) {
	return b.stopSandboxWithOptions(ctx, sandboxID, domain.SandboxTypeManual, options)
}

func (b *SandboxRPCBridge) stopSandbox(ctx context.Context, sandboxID, source string) (*domain.Sandbox, error) {
	outcome, err := b.stopSandboxWithOptions(ctx, sandboxID, source, sandboxes.StopOptions{Mode: sandboxes.StopModeForce})
	return outcome.Sandbox, err
}

func (b *SandboxRPCBridge) stopSandboxWithOptions(ctx context.Context, sandboxID, source string, options sandboxes.StopOptions) (sandboxes.StopOutcome, error) {
	session, err := b.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return sandboxes.StopOutcome{}, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.ReconcileRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile sandbox runtime state before stop", "sandbox_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	outcome, stopErr := b.sessionLifecycle().StopLoadedWithOptions(ctx, session, options)
	if outcome.Preparation.Error != nil {
		slog.Warn("graceful sandbox stop escalated to force", "sandbox_id", session.Summary.ID, "outcome", outcome.Preparation.Outcome, "error", outcome.Preparation.Error)
	}
	if outcome.DriverStopped && outcome.Sandbox != nil {
		b.publishSchedulerTopic("agent-compose.session.stopped", schedulers.SessionTopicPayload(outcome.Sandbox, source))
	}
	if stopErr != nil {
		return outcome, api.ConnectErrorForDomain(stopErr)
	}
	return outcome, nil
}

func (b *SandboxRPCBridge) indexCapabilitySandbox(session *domain.Sandbox) {
	if b != nil && b.capTokens != nil {
		b.capTokens.IndexSandbox(session, nil)
	}
}

func (b *SandboxRPCBridge) getSandbox(ctx context.Context, sandboxID string) (*domain.Sandbox, error) {
	session, err := b.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.ReconcileRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile sandbox runtime state during get", "sandbox_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	return session, nil
}

func (b *SandboxRPCBridge) listSandboxes(ctx context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error) {
	result, err := b.store.ListSandboxes(ctx, options)
	if err != nil {
		return domain.SandboxListResult{}, connect.NewError(connect.CodeInternal, err)
	}
	for index, sandbox := range result.Sandboxes {
		if reconciled, recErr := b.ReconcileRuntimeState(ctx, sandbox); recErr != nil {
			slog.Warn("failed to reconcile sandbox runtime state during list", "sandbox_id", sandbox.Summary.ID, "error", recErr)
		} else {
			result.Sandboxes[index] = reconciled
		}
	}
	return result, nil
}

func (b *SandboxRPCBridge) EnsureSessionProxyReady(ctx context.Context, sessionID string) (domain.ProxyState, error) {
	_, proxyState, err := b.sessionLifecycle().EnsureProxyReady(ctx, sessionID)
	return proxyState, err
}

func (b *SandboxRPCBridge) getSandboxProxy(ctx context.Context, sandboxID string) (*domain.Sandbox, api.SandboxProxy, error) {
	session, proxyState, err := b.sessionLifecycle().EnsureProxyReady(ctx, sandboxID)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, api.SandboxProxy{}, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, api.SandboxProxy{}, api.ConnectErrorForDomain(err)
	}
	notebookURL := session.Summary.ProxyPath
	if proxyState.Token != "" {
		notebookURL += "?token=" + url.QueryEscape(proxyState.Token)
	}
	return session, api.SandboxProxy{ProxyPath: session.Summary.ProxyPath, NotebookURL: notebookURL}, nil
}

func (b *SandboxRPCBridge) GetSandboxProxy(ctx context.Context, sandboxID string) (api.SandboxProxy, error) {
	_, proxy, err := b.getSandboxProxy(ctx, sandboxID)
	if err != nil {
		return api.SandboxProxy{}, err
	}
	return proxy, nil
}

func (b *SandboxRPCBridge) sessionLifecycle() sandboxes.Lifecycle {
	return sandboxes.Lifecycle{
		Config:           b.config,
		Store:            b.store,
		Workspace:        b.configDB,
		WorkspaceEnsurer: b.workspaceEnsurer,
		Driver:           b.driver,
		Liveness:         sandboxRuntimeLiveness{runtimes: b.runtimes},
		TokenRevoker:     b.configDB,
		AccessRevoker:    b.capTokens,
		Notifier: sandboxLifecycleNotifier{
			streams:   b.streams,
			dashboard: b.dashboard,
		},
		GuideWriter: func(ctx context.Context, session *domain.Sandbox, capsetIDs []string) {
			runs.WriteCapabilityGuide(ctx, runs.CapabilityGuideDeps{
				Provider:       b.cap,
				Store:          b.store,
				Streams:        b.streams,
				Config:         b.config,
				WriteGuestFile: b.agentExecutor.GuestFileWriterFor(session),
			}, session, capsetIDs)
		},
		PrepareAgentEnvironment: b.agentExecutor.PrepareSandboxAgentEnvironmentFromTags,
		Locks:                   b.lifecycleLocks,
	}
}

type sandboxRuntimeLiveness struct {
	runtimes RuntimeProvider
}

func (p sandboxRuntimeLiveness) IsSandboxAlive(ctx context.Context, driver string, session *domain.Sandbox, vmState domain.VMState) (bool, bool, error) {
	if p.runtimes == nil {
		return false, false, nil
	}
	runtime, err := p.runtimes.ForDriver(driver)
	if err != nil {
		return false, false, err
	}
	aliveRuntime, ok := runtime.(interface {
		IsSandboxAlive(context.Context, *domain.Sandbox, domain.VMState) (bool, error)
	})
	if !ok {
		return false, false, nil
	}
	alive, err := aliveRuntime.IsSandboxAlive(ctx, session, vmState)
	if errors.Is(err, domain.ErrUnsupported) {
		return false, false, nil
	}
	return alive, true, err
}

type sandboxLifecycleNotifier struct {
	streams   *sandboxes.StreamBroker
	dashboard *dashboard.Hub
}

func (n sandboxLifecycleNotifier) PublishSandboxUpdated(summary *domain.SandboxSummary) {
	if n.streams != nil {
		n.streams.PublishSandboxUpdated(summary)
	}
}

func (n sandboxLifecycleNotifier) PublishEventAdded(sessionID string, event domain.SandboxEvent) {
	if n.streams != nil {
		n.streams.PublishEventAdded(sessionID, event)
	}
}

func (n sandboxLifecycleNotifier) NotifyDashboard(reason string) {
	if n.dashboard != nil {
		n.dashboard.Notify(reason)
	}
}

func toSandboxWorkspaceSnapshot(item domain.WorkspaceConfig) *domain.SandboxWorkspace {
	return &domain.SandboxWorkspace{
		ID:            item.ID,
		Name:          item.Name,
		Type:          item.Type,
		ConfigJSON:    item.ConfigJSON,
		SnapshotID:    item.SnapshotID,
		SnapshotLease: item.SnapshotLease,
	}
}
