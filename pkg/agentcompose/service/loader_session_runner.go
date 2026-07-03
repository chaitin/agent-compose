package agentcompose

import (
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/workspaces"
	"agent-compose/pkg/capabilities"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

type LoaderSessionRunner struct {
	manager *LoaderManager
}

func NewLoaderSessionRunner(manager *LoaderManager) *LoaderSessionRunner {
	return &LoaderSessionRunner{manager: manager}
}

func (r *LoaderSessionRunner) Shutdown(ctx context.Context, sessionID string) error {
	m := r.manager
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	stopCtx := context.WithoutCancel(ctx)
	session, err := m.store.GetSession(stopCtx, sessionID)
	if err != nil {
		return err
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if err := m.driver.StopSessionVM(stopCtx, session); err != nil {
		return err
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := m.store.UpdateSession(stopCtx, session); err != nil {
		return err
	}
	m.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{ID: uuid.NewString(), Type: "session.stopped", Level: "info", Message: "session stopped", CreatedAt: time.Now().UTC()}
	_ = m.store.AddEvent(stopCtx, session.Summary.ID, event)
	m.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := m.store.GetSession(stopCtx, session.Summary.ID)
	if err != nil {
		return err
	}
	m.Publish("agent-compose.session.stopped", loaders.SessionTopicPayload(loaded, "loader"))
	return nil
}

func (r *LoaderSessionRunner) Ensure(ctx context.Context, loader Loader, request domain.LoaderAgentRequest, titleOverridesSession bool) (*Session, string, error) {
	m := r.manager
	agentDefinition, err := m.loaderAgentDefinition(ctx, loader)
	if err != nil {
		return nil, "", err
	}
	effectivePolicy := domain.NormalizeLoaderSessionPolicy(loader.Summary.SessionPolicy)
	if strings.TrimSpace(request.SessionPolicy) != "" {
		effectivePolicy = domain.NormalizeLoaderSessionPolicy(request.SessionPolicy)
	}
	hasOverrides := loaderAgentRequestOverridesSession(request, titleOverridesSession)
	forceNew := effectivePolicy == domain.LoaderSessionPolicyNew || hasOverrides
	if !forceNew {
		if binding, ok, err := m.configDB.GetLoaderBinding(ctx, loader.Summary.ID); err != nil {
			return nil, "", err
		} else if ok {
			session, eventType, err := r.LoadOrResume(ctx, binding.SessionID)
			if err == nil {
				return session, eventType, nil
			}
			slogWarnReuseLoaderStickySession(loader.Summary.ID, binding.SessionID, err)
		}
	}

	envItems, err := m.configDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, "", err
	}
	if agentDefinition != nil {
		envItems = domain.MergeEnvItems(envItems, agentDefinition.EnvItems)
	}
	envItems = domain.MergeEnvItems(envItems, loader.EnvItems)
	envItems = domain.MergeEnvItems(envItems, request.SessionEnv)
	providerEnvItems := envItems
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	capabilityVars, capabilityTags := capabilities.BuildGatewaySessionVars(capabilities.ProxyTarget(m.cap), loader.Summary.CapsetIDs)
	envItems = domain.MergeEnvItems(envItems, capabilityVars)
	tags := []SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: loader.Summary.ID}, {Name: "loader_name", Value: loader.Summary.Name}}
	tags = append(tags, capabilityTags...)

	workspaceID := r.workspaceID(loader, request, agentDefinition)
	workspaceSnapshot, err := r.workspaceSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	driver, err := r.driver(request, loader, agentDefinition)
	if err != nil {
		return nil, "", err
	}
	guestImage := r.guestImage(request, loader, agentDefinition, driver)
	title := firstNonEmpty(strings.TrimSpace(request.Title), strings.TrimSpace(loader.Summary.Name), domain.DefaultLoaderName(time.Now().UTC()))
	if agentDefinition != nil {
		tags = append(tags, sessionTagsFromProto(api.AgentDefinitionTagsToProto(*agentDefinition))...)
	}
	session, err := m.store.CreateSession(ctx, title, "", driver, guestImage, workspaceID, domain.SessionTypeScript+":"+loader.Summary.ID, workspaceSnapshot, envItems, tags)
	if err != nil {
		return nil, "", err
	}
	session.ProviderEnvItems = providerEnvItems
	if err := workspaces.PrepareSessionWorkspace(ctx, m.config, m.configDB, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = m.store.UpdateSession(ctx, session)
		return nil, "", err
	}
	writeCapabilityGuide(ctx, m.cap, m.store, m.streams, session, loader.Summary.CapsetIDs)
	if err := m.driver.StartSessionVM(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = m.store.UpdateSession(ctx, session)
		return nil, "", err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := m.store.UpdateSession(ctx, session); err != nil {
		return nil, "", err
	}
	m.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{ID: uuid.NewString(), Type: "session.created", Level: "info", Message: fmt.Sprintf("session started with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = m.store.AddEvent(ctx, session.Summary.ID, event)
	m.streams.PublishEventAdded(session.Summary.ID, event)
	if effectivePolicy == domain.LoaderSessionPolicySticky {
		_ = m.configDB.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: loader.Summary.ID, SessionID: session.Summary.ID})
	}
	loaded, err := m.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	restoreSessionTransientFields(loaded, session)
	m.Publish("agent-compose.session.created", map[string]any{
		"sessionId":     loaded.Summary.ID,
		"title":         loaded.Summary.Title,
		"driver":        loaded.Summary.Driver,
		"triggerSource": loaded.Summary.TriggerSource,
		"source":        "loader",
		"loaderId":      loader.Summary.ID,
	})
	return loaded, "loader.session.created", nil
}

func (r *LoaderSessionRunner) LoadOrResume(ctx context.Context, sessionID string) (*Session, string, error) {
	m := r.manager
	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if session.Summary.VMStatus == domain.VMStatusRunning {
		return session, "", nil
	}
	if err := workspaces.PrepareSessionWorkspace(ctx, m.config, m.configDB, session); err != nil {
		return nil, "", err
	}
	writeCapabilityGuide(ctx, m.cap, m.store, m.streams, session, capabilities.SessionCapsets(session))
	if err := m.driver.StartSessionVM(ctx, session); err != nil {
		return nil, "", err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := m.store.UpdateSession(ctx, session); err != nil {
		return nil, "", err
	}
	m.streams.PublishSessionUpdated(&session.Summary)
	event := SessionEvent{ID: uuid.NewString(), Type: "session.resumed", Level: "info", Message: fmt.Sprintf("session resumed with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = m.store.AddEvent(ctx, session.Summary.ID, event)
	m.streams.PublishEventAdded(session.Summary.ID, event)
	loaded, err := m.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	restoreSessionTransientFields(loaded, session)
	m.Publish("agent-compose.session.resumed", map[string]any{
		"sessionId": loaded.Summary.ID,
		"title":     loaded.Summary.Title,
		"driver":    loaded.Summary.Driver,
		"source":    "loader",
	})
	return loaded, "loader.session.resumed", nil
}

func (r *LoaderSessionRunner) workspaceID(loader Loader, request domain.LoaderAgentRequest, agentDefinition *domain.AgentDefinition) string {
	workspaceID := firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID))
	if agentDefinition != nil {
		workspaceID = firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID), strings.TrimSpace(agentDefinition.WorkspaceID))
	}
	return workspaceID
}

func (r *LoaderSessionRunner) workspaceSnapshot(ctx context.Context, workspaceID string) (*SessionWorkspace, error) {
	if workspaceID == "" {
		return nil, nil
	}
	workspaceConfig, err := r.manager.configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return toSessionWorkspaceSnapshot(workspaceConfig), nil
}

func (r *LoaderSessionRunner) driver(request domain.LoaderAgentRequest, loader Loader, agentDefinition *domain.AgentDefinition) (string, error) {
	driverValue := firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver))
	if agentDefinition != nil {
		driverValue = firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver), strings.TrimSpace(agentDefinition.Driver))
	}
	return driverpkg.ResolveSessionRuntimeDriver(driverValue, r.manager.config.RuntimeDriver)
}

func (r *LoaderSessionRunner) guestImage(request domain.LoaderAgentRequest, loader Loader, agentDefinition *domain.AgentDefinition, driver string) string {
	agentGuestImage := ""
	if agentDefinition != nil {
		agentGuestImage = agentDefinition.GuestImage
	}
	return driverpkg.ResolveSessionGuestImage(request.GuestImage, loader.Summary.GuestImage, agentGuestImage, driverpkg.DefaultGuestImageForDriver(r.manager.config, driver))
}

func (m *LoaderManager) sessionRunnerComponent() *LoaderSessionRunner {
	m.initLoaderComponents()
	return m.sessionRunner
}

func (m *LoaderManager) shutdownLoaderSession(ctx context.Context, sessionID string) error {
	return m.sessionRunnerComponent().Shutdown(ctx, sessionID)
}

func (m *LoaderManager) ensureLoaderSession(ctx context.Context, loader Loader, request domain.LoaderAgentRequest) (*Session, string, error) {
	return m.sessionRunnerComponent().Ensure(ctx, loader, request, true)
}

func (m *LoaderManager) loadOrResumeLoaderSession(ctx context.Context, sessionID string) (*Session, string, error) {
	return m.sessionRunnerComponent().LoadOrResume(ctx, sessionID)
}

func sessionTagsFromProto(items []*agentcomposev1.SessionTag) []SessionTag {
	return api.SessionTagsFromProto(items)
}

func slogWarnReuseLoaderStickySession(loaderID, sessionID string, err error) {
	slog.Warn("failed to reuse loader sticky session, creating a new one", "loader_id", loaderID, "session_id", sessionID, "error", err)
}
