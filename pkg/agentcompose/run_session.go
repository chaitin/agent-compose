package agentcompose

import (
	"context"
	"fmt"
	"strings"
	"time"

	driverpkg "agent-compose/pkg/driver"

	"github.com/google/uuid"
)

type ProjectRunSessionResult struct {
	Session *Session
	Created bool
}

func (s *Service) ensureProjectRunSession(ctx context.Context, run ProjectRunRecord, prepared ProjectRunPreparation, requestedSessionID string) (ProjectRunSessionResult, error) {
	if s == nil || s.config == nil || s.store == nil || s.driver == nil {
		return ProjectRunSessionResult{}, fmt.Errorf("session runtime dependencies are required")
	}
	tags := projectRunSessionTags(run)
	capabilityVars, capabilityTags := buildCapabilityGatewaySessionVars(capabilityGatewayProxyTarget(s.cap), prepared.CapsetIDs)
	tags = append(tags, capabilityTags...)
	if sessionID := strings.TrimSpace(requestedSessionID); sessionID != "" {
		session, err := s.store.GetSession(ctx, sessionID)
		if err != nil {
			return ProjectRunSessionResult{}, fmt.Errorf("load session %s: %w", sessionID, err)
		}
		if session.Summary.VMStatus != VMStatusRunning {
			driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, s.config.RuntimeDriver)
			if err != nil {
				return ProjectRunSessionResult{}, err
			}
			guestImage := driverpkg.ResolveSessionGuestImage(session.Summary.GuestImage, driverpkg.DefaultGuestImageForDriver(s.config, driver))
			if err := s.ensureDriverImage(ctx, driverImageEnsureRequest{
				Driver:      driver,
				ImageRef:    guestImage,
				ProjectName: run.ProjectName,
				AgentName:   run.AgentName,
			}); err != nil {
				return ProjectRunSessionResult{Session: session}, err
			}
		}
		session.EnvItems = mergeEnvItems(session.EnvItems, capabilityVars)
		session.Summary.Tags = mergeSessionTags(session.Summary.Tags, tags)
		if err := s.startProjectRunSession(ctx, session, "session.resumed", "session resumed for project run"); err != nil {
			return ProjectRunSessionResult{Session: session}, err
		}
		return ProjectRunSessionResult{Session: session}, nil
	}

	workspaceID := ""
	if prepared.Workspace != nil {
		workspaceID = strings.TrimSpace(prepared.Workspace.ID)
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(run.Driver, s.config.RuntimeDriver)
	if err != nil {
		return ProjectRunSessionResult{}, err
	}
	guestImage := driverpkg.ResolveSessionGuestImage(run.ImageRef, driverpkg.DefaultGuestImageForDriver(s.config, driver))
	if err := s.ensureDriverImage(ctx, driverImageEnsureRequest{
		Driver:      driver,
		ImageRef:    guestImage,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
	}); err != nil {
		return ProjectRunSessionResult{}, err
	}
	session, err := s.store.CreateSession(ctx,
		projectRunSessionTitle(run),
		"",
		driver,
		guestImage,
		workspaceID,
		SessionTypeManual,
		prepared.Workspace,
		mergeEnvItems(prepared.EnvItems, capabilityVars),
		tags,
	)
	if err != nil {
		return ProjectRunSessionResult{}, err
	}
	session.ProviderEnvItems = prepared.ProviderEnvItems
	if err := s.startProjectRunSession(ctx, session, "session.created", "session started for project run"); err != nil {
		return ProjectRunSessionResult{Session: session, Created: true}, err
	}
	return ProjectRunSessionResult{Session: session, Created: true}, nil
}

func (s *Service) startProjectRunSession(ctx context.Context, session *Session, eventType, eventMessage string) error {
	if session == nil {
		return fmt.Errorf("session is required")
	}
	if err := prepareSessionWorkspace(ctx, s.config, s.configDB, session); err != nil {
		session.Summary.VMStatus = VMStatusFailed
		_ = s.store.UpdateSession(ctx, session)
		return err
	}
	writeCapabilityGuide(ctx, s.cap, s.store, s.streams, session, sessionCapabilityCapsets(session))
	if session.Summary.VMStatus != VMStatusRunning {
		if err := s.driver.StartSessionVM(ctx, session); err != nil {
			session.Summary.VMStatus = VMStatusFailed
			_ = s.store.UpdateSession(ctx, session)
			return err
		}
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := s.store.UpdateSession(ctx, session); err != nil {
		return err
	}
	s.publishProjectRunSessionStarted(ctx, session, eventType, eventMessage)
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	restoreSessionTransientFields(loaded, session)
	*session = *loaded
	return nil
}

func (s *Service) publishProjectRunSessionStarted(ctx context.Context, session *Session, eventType, message string) {
	if s.streams != nil {
		s.streams.PublishSessionUpdated(&session.Summary)
	}
	if s.dashboard != nil {
		s.dashboard.Notify("session_updated")
	}
	event := SessionEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     "info",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	_ = s.store.AddEvent(ctx, session.Summary.ID, event)
	if s.streams != nil {
		s.streams.PublishEventAdded(session.Summary.ID, event)
	}
	if s.bus != nil {
		topic := "agent-compose.session.created"
		if eventType == "session.resumed" {
			topic = "agent-compose.session.resumed"
		}
		s.bus.Publish(LoaderTopicEvent{
			Topic:     topic,
			Payload:   sessionTopicPayload(session, "project-run"),
			CreatedAt: time.Now().UTC(),
		})
	}
}

func projectRunSessionTitle(run ProjectRunRecord) string {
	project := strings.TrimSpace(run.ProjectName)
	if project == "" {
		project = strings.TrimSpace(run.ProjectID)
	}
	target := strings.TrimSpace(run.TargetName)
	if target == "" {
		target = strings.TrimSpace(run.AgentName)
	}
	if target == "" {
		target = "run"
	}
	return strings.TrimSpace(fmt.Sprintf("%s/%s run", project, target))
}

func projectRunSessionTags(run ProjectRunRecord) []SessionTag {
	tags := []SessionTag{
		{Name: "project", Value: strings.TrimSpace(run.ProjectID)},
		{Name: "target_type", Value: strings.TrimSpace(run.TargetType)},
		{Name: "target_name", Value: strings.TrimSpace(run.TargetName)},
		{Name: "run_id", Value: strings.TrimSpace(run.RunID)},
		{Name: "source", Value: normalizeProjectRunSource(run.Source)},
	}
	if agentName := strings.TrimSpace(run.AgentName); agentName != "" {
		tags = append(tags, SessionTag{Name: "agent", Value: agentName})
	}
	if schedulerID := strings.TrimSpace(run.SchedulerID); schedulerID != "" {
		tags = append(tags, SessionTag{Name: "scheduler_id", Value: schedulerID})
	}
	return tags
}

func mergeSessionTags(existing, additions []SessionTag) []SessionTag {
	result := append([]SessionTag(nil), existing...)
	for _, addition := range additions {
		addition.Name = strings.TrimSpace(addition.Name)
		addition.Value = strings.TrimSpace(addition.Value)
		if addition.Name == "" {
			continue
		}
		found := false
		for _, current := range result {
			if strings.TrimSpace(current.Name) == addition.Name && strings.TrimSpace(current.Value) == addition.Value {
				found = true
				break
			}
		}
		if !found {
			result = append(result, addition)
		}
	}
	return result
}
