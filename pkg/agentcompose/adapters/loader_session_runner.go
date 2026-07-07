package adapters

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	"agent-compose/pkg/workspaces"
)

type LoaderVolumeResolver interface {
	ResolveMounts(ctx context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SessionVolumeMount, []string, error)
}

type LoaderSessionRunner struct {
	Config    *appconfig.Config
	Store     *sessionstore.Store
	ConfigDB  *configstore.ConfigStore
	Driver    sessions.SessionDriver
	Cap       capabilities.Provider
	Volumes   LoaderVolumeResolver
	Streams   *sessions.StreamBroker
	Publisher loaders.ControllerPublisher
}

func NewLoaderSessionRunner(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, driver sessions.SessionDriver, cap capabilities.Provider, volumeResolver LoaderVolumeResolver, streams *sessions.StreamBroker, publisher loaders.ControllerPublisher) *LoaderSessionRunner {
	return &LoaderSessionRunner{Config: config, Store: store, ConfigDB: configDB, Driver: driver, Cap: cap, Volumes: volumeResolver, Streams: streams, Publisher: publisher}
}

func (r *LoaderSessionRunner) Shutdown(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	stopCtx := context.WithoutCancel(ctx)
	session, err := r.Store.GetSession(stopCtx, sessionID)
	if err != nil {
		return err
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if err := r.Driver.StopSessionVM(stopCtx, session); err != nil {
		return err
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := r.Store.UpdateSession(stopCtx, session); err != nil {
		return err
	}
	if r.Streams != nil {
		r.Streams.PublishSessionUpdated(&session.Summary)
	}
	event := domain.SessionEvent{ID: uuid.NewString(), Type: "session.stopped", Level: "info", Message: "session stopped", CreatedAt: time.Now().UTC()}
	_ = r.Store.AddEvent(stopCtx, session.Summary.ID, event)
	if r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	loaded, err := r.Store.GetSession(stopCtx, session.Summary.ID)
	if err != nil {
		return err
	}
	r.publish("agent-compose.session.stopped", loaders.SessionTopicPayload(loaded, "loader"))
	return nil
}

func (r *LoaderSessionRunner) Ensure(ctx context.Context, loader domain.Loader, request domain.LoaderAgentRequest, titleOverridesSession bool) (*domain.Session, string, error) {
	agentDefinition, err := r.ResolveLoaderAgentDefinition(ctx, loader)
	if err != nil {
		return nil, "", err
	}
	effectivePolicy := domain.NormalizeLoaderSessionPolicy(loader.Summary.SessionPolicy)
	if strings.TrimSpace(request.SessionPolicy) != "" {
		effectivePolicy = domain.NormalizeLoaderSessionPolicy(request.SessionPolicy)
	}
	hasOverrides := loaders.AgentRequestOverridesSession(request, titleOverridesSession)
	forceNew := effectivePolicy == domain.LoaderSessionPolicyNew || hasOverrides
	if !forceNew {
		if binding, ok, err := r.ConfigDB.GetLoaderBinding(ctx, loader.Summary.ID); err != nil {
			return nil, "", err
		} else if ok {
			session, eventType, err := r.LoadOrResume(ctx, binding.SessionID)
			if err == nil {
				return session, eventType, nil
			}
			slog.Warn("failed to reuse loader sticky session, creating a new one", "loader_id", loader.Summary.ID, "session_id", binding.SessionID, "error", err)
		}
	}

	envItems, err := r.ConfigDB.ListGlobalEnv(ctx)
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
	capabilityVars, capabilityTags := capabilities.BuildGatewaySessionVars(capabilities.ProxyTarget(r.Cap), loader.Summary.CapsetIDs)
	envItems = domain.MergeEnvItems(envItems, capabilityVars)
	tags := []domain.SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: loader.Summary.ID}, {Name: "loader_name", Value: loader.Summary.Name}}
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
		tags = append(tags, api.SessionTagsFromProto(api.AgentDefinitionTagsToProto(*agentDefinition))...)
	}
	volumeMounts, volumeWarnings, err := r.resolveVolumeMounts(ctx, loader, request, agentDefinition)
	if err != nil {
		return nil, "", err
	}
	session, err := r.Store.CreateSessionWithOptions(ctx, title, "", driver, guestImage, workspaceID, domain.SessionTypeScript+":"+loader.Summary.ID, workspaceSnapshot, envItems, tags, sessionstore.CreateSessionOptions{
		VolumeMounts: volumeMounts,
	})
	if err != nil {
		return nil, "", err
	}
	session.ProviderEnvItems = providerEnvItems
	r.recordVolumeWarnings(ctx, session.Summary.ID, volumeWarnings)
	if err := workspaces.PrepareSessionWorkspace(ctx, r.Config, r.ConfigDB, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSession(ctx, session)
		return nil, "", err
	}
	writeCapabilityGuide(ctx, r.Cap, r.Store, r.Streams, session, loader.Summary.CapsetIDs)
	if err := r.Driver.StartSessionVM(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSession(ctx, session)
		return nil, "", err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := r.Store.UpdateSession(ctx, session); err != nil {
		return nil, "", err
	}
	if r.Streams != nil {
		r.Streams.PublishSessionUpdated(&session.Summary)
	}
	event := domain.SessionEvent{ID: uuid.NewString(), Type: "session.created", Level: "info", Message: fmt.Sprintf("session started with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = r.Store.AddEvent(ctx, session.Summary.ID, event)
	if r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	if effectivePolicy == domain.LoaderSessionPolicySticky {
		_ = r.ConfigDB.UpsertLoaderBinding(ctx, domain.LoaderBinding{LoaderID: loader.Summary.ID, SessionID: session.Summary.ID})
	}
	loaded, err := r.Store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	domain.RestoreSessionTransientFields(loaded, session)
	r.publish("agent-compose.session.created", map[string]any{
		"sessionId":     loaded.Summary.ID,
		"title":         loaded.Summary.Title,
		"driver":        loaded.Summary.Driver,
		"triggerSource": loaded.Summary.TriggerSource,
		"source":        "loader",
		"loaderId":      loader.Summary.ID,
	})
	return loaded, "loader.session.created", nil
}

func (r *LoaderSessionRunner) Load(ctx context.Context, sessionID string) (*domain.Session, error) {
	return r.Store.GetSession(ctx, sessionID)
}

func (r *LoaderSessionRunner) LoadOrResume(ctx context.Context, sessionID string) (*domain.Session, string, error) {
	session, err := r.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if session.Summary.VMStatus == domain.VMStatusRunning {
		return session, "", nil
	}
	if err := workspaces.PrepareSessionWorkspace(ctx, r.Config, r.ConfigDB, session); err != nil {
		return nil, "", err
	}
	writeCapabilityGuide(ctx, r.Cap, r.Store, r.Streams, session, capabilities.SessionCapsets(session))
	if err := r.Driver.StartSessionVM(ctx, session); err != nil {
		return nil, "", err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := r.Store.UpdateSession(ctx, session); err != nil {
		return nil, "", err
	}
	if r.Streams != nil {
		r.Streams.PublishSessionUpdated(&session.Summary)
	}
	event := domain.SessionEvent{ID: uuid.NewString(), Type: "session.resumed", Level: "info", Message: fmt.Sprintf("session resumed with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = r.Store.AddEvent(ctx, session.Summary.ID, event)
	if r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	loaded, err := r.Store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	domain.RestoreSessionTransientFields(loaded, session)
	r.publish("agent-compose.session.resumed", map[string]any{
		"sessionId": loaded.Summary.ID,
		"title":     loaded.Summary.Title,
		"driver":    loaded.Summary.Driver,
		"source":    "loader",
	})
	return loaded, "loader.session.resumed", nil
}

func (r *LoaderSessionRunner) ResolveLoaderAgentDefinition(ctx context.Context, loader domain.Loader) (*domain.AgentDefinition, error) {
	agentID := strings.TrimSpace(loader.Summary.AgentID)
	if agentID == "" {
		return nil, nil
	}
	agent, err := r.ConfigDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("loader agent definition %s: %w", agentID, err)
	}
	if !agent.Enabled {
		return nil, fmt.Errorf("loader agent definition %s is disabled", agentID)
	}
	return &agent, nil
}

func (r *LoaderSessionRunner) workspaceID(loader domain.Loader, request domain.LoaderAgentRequest, agentDefinition *domain.AgentDefinition) string {
	workspaceID := firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID))
	if agentDefinition != nil {
		workspaceID = firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID), strings.TrimSpace(agentDefinition.WorkspaceID))
	}
	return workspaceID
}

func (r *LoaderSessionRunner) workspaceSnapshot(ctx context.Context, workspaceID string) (*domain.SessionWorkspace, error) {
	if workspaceID == "" {
		return nil, nil
	}
	workspaceConfig, err := r.ConfigDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return toSessionWorkspaceSnapshot(workspaceConfig), nil
}

func (r *LoaderSessionRunner) driver(request domain.LoaderAgentRequest, loader domain.Loader, agentDefinition *domain.AgentDefinition) (string, error) {
	driverValue := firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver))
	if agentDefinition != nil {
		driverValue = firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver), strings.TrimSpace(agentDefinition.Driver))
	}
	return driverpkg.ResolveSessionRuntimeDriver(driverValue, r.Config.RuntimeDriver)
}

func (r *LoaderSessionRunner) guestImage(request domain.LoaderAgentRequest, loader domain.Loader, agentDefinition *domain.AgentDefinition, driver string) string {
	agentGuestImage := ""
	if agentDefinition != nil {
		agentGuestImage = agentDefinition.GuestImage
	}
	return driverpkg.ResolveSessionGuestImage(request.GuestImage, loader.Summary.GuestImage, agentGuestImage, driverpkg.DefaultGuestImageForDriver(r.Config, driver))
}

func (r *LoaderSessionRunner) resolveVolumeMounts(ctx context.Context, loader domain.Loader, request domain.LoaderAgentRequest, agentDefinition *domain.AgentDefinition) ([]domain.SessionVolumeMount, []string, error) {
	specs, err := mergeLoaderVolumeMountSpecs(agentDefinitionVolumes(agentDefinition), loader.Volumes, request.Volumes)
	if err != nil {
		return nil, nil, err
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}
	if r.Volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	return r.Volumes.ResolveMounts(ctx, specs, volumes.ResolveOptions{})
}

func mergeLoaderVolumeMountSpecs(groups ...[]domain.VolumeMountSpec) ([]domain.VolumeMountSpec, error) {
	var merged []domain.VolumeMountSpec
	byTarget := make(map[string]int)
	for _, group := range groups {
		normalized, err := domain.NormalizeVolumeMountSpecs(group)
		if err != nil {
			return nil, err
		}
		for _, spec := range normalized {
			target := filepath.Clean(spec.Target)
			if index, ok := byTarget[target]; ok {
				merged[index] = spec
				continue
			}
			byTarget[target] = len(merged)
			merged = append(merged, spec)
		}
	}
	return merged, nil
}

func agentDefinitionVolumes(agentDefinition *domain.AgentDefinition) []domain.VolumeMountSpec {
	if agentDefinition == nil {
		return nil
	}
	return agentDefinition.Volumes
}

func (r *LoaderSessionRunner) recordVolumeWarnings(ctx context.Context, sessionID string, warnings []string) {
	if r == nil || r.Store == nil || len(warnings) == 0 {
		return
	}
	for _, warning := range warnings {
		event := domain.SessionEvent{ID: uuid.NewString(), Type: "session.volume.warning", Level: "warn", Message: warning, CreatedAt: time.Now().UTC()}
		_ = r.Store.AddEvent(ctx, sessionID, event)
		if r.Streams != nil {
			r.Streams.PublishEventAdded(sessionID, event)
		}
	}
}

func (r *LoaderSessionRunner) publish(topic string, payload map[string]any) {
	if r.Publisher != nil {
		_ = r.Publisher.Publish(domain.LoaderTopicEvent{
			Topic:     strings.TrimSpace(topic),
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		})
	}
}
