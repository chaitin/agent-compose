package adapters

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
)

type SandboxRPCBridge struct {
	config           *appconfig.Config
	store            *sessionstore.Store
	configDB         *configstore.ConfigStore
	workspaceEnsurer workspaces.WorkspaceEnsurer
	driver           sessions.SandboxDriver
	runtimes         RuntimeProvider
	bus              *loaders.Bus
	streams          *sessions.StreamBroker
	cap              capabilities.Provider
	capTokens        *CapabilitySandboxResolver
	dashboard        *dashboard.Hub
	agentExecutor    *AgentExecutor
	lifecycleLocks   *sessions.LifecycleLocks
}

func NewSandboxRPCBridge(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, workspaceEnsurer workspaces.WorkspaceEnsurer, driver sessions.SandboxDriver, runtimes RuntimeProvider, bus *loaders.Bus, streams *sessions.StreamBroker, cap capabilities.Provider, capTokens *CapabilitySandboxResolver, dashboard *dashboard.Hub, agentExecutor *AgentExecutor, locks ...*sessions.LifecycleLocks) *SandboxRPCBridge {
	bridge := &SandboxRPCBridge{
		config:           config,
		store:            store,
		configDB:         configDB,
		workspaceEnsurer: workspaceEnsurer,
		driver:           driver,
		runtimes:         runtimes,
		bus:              bus,
		streams:          streams,
		cap:              cap,
		capTokens:        capTokens,
		dashboard:        dashboard,
		agentExecutor:    agentExecutor,
	}
	if len(locks) > 0 {
		bridge.lifecycleLocks = locks[0]
	}
	return bridge
}

func (b *SandboxRPCBridge) SubscribeSandbox(sandboxID string) (<-chan sessions.WatchEvent, func()) {
	return b.streams.Subscribe(sandboxID)
}

func (b *SandboxRPCBridge) CallJSON(ctx context.Context, method, requestJSON string) (string, error) {
	return b.CallJSONWithSource(ctx, method, requestJSON, domain.SandboxTypeScript)
}

func (b *SandboxRPCBridge) CallJSONWithSource(ctx context.Context, method, requestJSON, source string) (string, error) {
	method = strings.TrimSpace(method)
	switch method {
	case "CreateSession":
		var request sandboxRPCCreateRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		loaded, err := b.createSandboxWithAgent(ctx, request, source, loaders.SandboxCreationContextFromContext(ctx))
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Session: sandboxRPCDetailFromDomain(loaded)})
	case "ResumeSession":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, err := b.resumeSandbox(ctx, request.ID(), source)
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Session: sandboxRPCDetailFromDomain(sandbox)})
	case "StopSession":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, err := b.stopSandbox(ctx, request.ID(), source)
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Session: sandboxRPCDetailFromDomain(sandbox)})
	case "GetSession":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, err := b.getSandbox(ctx, request.ID())
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCResponse{Session: sandboxRPCDetailFromDomain(sandbox)})
	case "ListSessions":
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
			response.Sessions = append(response.Sessions, sandboxRPCSummaryFromDomain(&sandbox.Summary))
		}
		return encodeSandboxRPCJSON(response)
	case "GetSessionProxy":
		var request sandboxRPCIDRequest
		if err := decodeSandboxRPCJSON(requestJSON, &request); err != nil {
			return "", err
		}
		sandbox, proxy, err := b.getSandboxProxy(ctx, request.ID())
		if err != nil {
			return "", err
		}
		return encodeSandboxRPCJSON(sandboxRPCProxyResponse{SessionID: sandbox.Summary.ID, ProxyPath: proxy.ProxyPath, NotebookURL: proxy.NotebookURL, Driver: sandbox.Summary.Driver, VMStatus: sandbox.Summary.VMStatus})
	default:
		return "", fmt.Errorf("unsupported session rpc %q", method)
	}
}

func (b *SandboxRPCBridge) publishLoaderTopic(topic string, payload map[string]any) {
	if b == nil || b.bus == nil {
		return
	}
	b.bus.Publish(domain.LoaderTopicEvent{
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	})
}

func (b *SandboxRPCBridge) createSandbox(ctx context.Context, req sandboxRPCCreateRequest, source string) (*domain.Sandbox, error) {
	return b.createSandboxWithAgent(ctx, req, source, loaders.SandboxCreationContext{})
}

func (b *SandboxRPCBridge) createSandboxWithAgent(ctx context.Context, req sandboxRPCCreateRequest, source string, creation loaders.SandboxCreationContext) (*domain.Sandbox, error) {
	agentConfig := execution.AgentConfig{Provider: domain.NormalizeAgentKind(creation.Provider)}
	var agentDefinition *domain.AgentDefinition
	if agentID := strings.TrimSpace(creation.AgentDefinitionID); agentID != "" {
		definition, err := b.configDB.GetAgentDefinition(ctx, agentID)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("resolve sandbox agent definition %s: %w", agentID, err))
		}
		if !definition.Enabled {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox agent definition %s is disabled", agentID))
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
		return nil, connect.NewError(connect.CodeInternal, err)
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
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		workspaceSnapshot = toSandboxWorkspaceSnapshot(workspaceConfig)
	}

	driver, err := driverpkg.ResolveSandboxRuntimeDriver(req.Driver, b.config.RuntimeDriver)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := driverpkg.ValidateCompiledRuntimeDriver(driver); err != nil {
		return nil, api.ConnectErrorForDomain(classifyRuntimeProviderError(err))
	}
	guestImage := driverpkg.ResolveSandboxGuestImage(req.GuestImage, driverpkg.DefaultGuestImageForDriver(b.config, driver))
	session, err := b.store.CreateSandbox(ctx, req.Title, req.BaseWorkspace, driver, guestImage, workspaceID, source, workspaceSnapshot, envItems, tags)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	llms.SetSandboxProviderEnvItems(session, providerEnvItems)
	if err := b.workspaceEnsurer.Ensure(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = b.store.UpdateSandbox(ctx, session)
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	writeCapabilityGuide(ctx, b.cap, b.store, b.streams, session, req.CapsetIDs)
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
	b.publishLoaderTopic("agent-compose.session.created", loaders.SessionTopicPayload(loaded, source))
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
	b.publishLoaderTopic("agent-compose.session.resumed", loaders.SessionTopicPayload(loaded, source))
	return loaded, nil
}

func (b *SandboxRPCBridge) ReconcileRuntimeState(ctx context.Context, session *domain.Sandbox) (*domain.Sandbox, error) {
	reconciled, err := b.sessionLifecycle().ReconcileRuntimeState(ctx, session)
	if err == nil && reconciled != nil && reconciled.Summary.VMStatus != domain.VMStatusRunning {
		b.revokeCapabilitySandbox(reconciled.Summary.ID)
	}
	return reconciled, err
}

func (b *SandboxRPCBridge) StopSandbox(ctx context.Context, sandboxID string) (*domain.Sandbox, error) {
	return b.stopSandbox(ctx, sandboxID, domain.SandboxTypeManual)
}

func (b *SandboxRPCBridge) stopSandbox(ctx context.Context, sandboxID, source string) (*domain.Sandbox, error) {
	session, err := b.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	if reconciled, recErr := b.ReconcileRuntimeState(ctx, session); recErr != nil {
		slog.Warn("failed to reconcile sandbox runtime state before stop", "sandbox_id", session.Summary.ID, "error", recErr)
	} else {
		session = reconciled
	}
	loaded, stopped, err := b.sessionLifecycle().StopLoaded(ctx, session)
	if err != nil {
		return nil, api.ConnectErrorForDomain(err)
	}
	b.revokeCapabilitySandbox(loaded.Summary.ID)
	if stopped {
		b.publishLoaderTopic("agent-compose.session.stopped", loaders.SessionTopicPayload(loaded, source))
	}
	return loaded, nil
}

func (b *SandboxRPCBridge) indexCapabilitySandbox(session *domain.Sandbox) {
	if b != nil && b.capTokens != nil {
		b.capTokens.IndexSandbox(session)
	}
}

func (b *SandboxRPCBridge) revokeCapabilitySandbox(sandboxID string) {
	if b != nil && b.capTokens != nil {
		b.capTokens.RevokeSandbox(sandboxID)
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

func (b *SandboxRPCBridge) sessionLifecycle() sessions.Lifecycle {
	return sessions.Lifecycle{
		Config:           b.config,
		Store:            b.store,
		Workspace:        b.configDB,
		WorkspaceEnsurer: b.workspaceEnsurer,
		Driver:           b.driver,
		Liveness:         sandboxRuntimeLiveness{runtimes: b.runtimes},
		TokenRevoker:     b.configDB,
		Notifier: sandboxLifecycleNotifier{
			streams:   b.streams,
			dashboard: b.dashboard,
		},
		GuideWriter: func(ctx context.Context, session *domain.Sandbox, capsetIDs []string) {
			writeCapabilityGuide(ctx, b.cap, b.store, b.streams, session, capsetIDs)
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
	return alive, true, err
}

type sandboxLifecycleNotifier struct {
	streams   *sessions.StreamBroker
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

func writeCapabilityGuide(ctx context.Context, provider capabilities.Provider, store *sessionstore.Store, streams *sessions.StreamBroker, session *domain.Sandbox, capsetIDs []string) {
	ids := capabilities.NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 || provider == nil || session == nil {
		return
	}
	catalogPath := capabilities.SandboxGuidePath(session)
	if catalogPath == "" {
		return
	}
	var b strings.Builder
	rendered := false
	for _, id := range ids {
		guide, err := capabilities.CapabilityGuideForScope(ctx, provider, capabilities.GuideScopeFromSandbox(session), id)
		if err != nil {
			slog.Warn("capability guide render skipped", "capset", id, "sandbox_id", session.Summary.ID, "error", err)
			recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, fmt.Sprintf("capability guide render skipped for capset %s", id))
			continue
		}
		if rendered {
			b.WriteString("\n\n")
		}
		b.Write(guide)
		rendered = true
	}
	if !rendered {
		return
	}
	content := b.String()
	if preamble := capabilities.GuidePreamble(capabilities.ProxyTarget(provider)); preamble != "" {
		content = preamble + content
	}
	if err := os.MkdirAll(filepath.Dir(catalogPath), 0o755); err != nil {
		slog.Warn("capability guide dir create failed", "sandbox_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide directory create failed")
		return
	}
	if err := os.WriteFile(catalogPath, []byte(content), 0o644); err != nil {
		slog.Warn("capability guide write failed", "sandbox_id", session.Summary.ID, "error", err)
		recordCapabilityGuideWarning(ctx, store, streams, session.Summary.ID, "capability guide write failed")
	}
}

func recordCapabilityGuideWarning(ctx context.Context, store *sessionstore.Store, streams *sessions.StreamBroker, sessionID, message string) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      "capability.guide.warning",
		Level:     "warning",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.AddEvent(ctx, sessionID, event); err != nil {
		slog.Warn("capability guide warning event failed", "sandbox_id", sessionID, "error", err)
		return
	}
	if streams != nil {
		streams.PublishEventAdded(sessionID, event)
	}
}

func toSandboxWorkspaceSnapshot(item domain.WorkspaceConfig) *domain.SandboxWorkspace {
	return &domain.SandboxWorkspace{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}
