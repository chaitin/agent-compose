package agentcompose

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/agentcompose/capabilities"
	"agent-compose/pkg/agentcompose/domain"
	"agent-compose/pkg/agentcompose/images"
	"agent-compose/pkg/agentcompose/loaders"
	"agent-compose/pkg/agentcompose/runs"
	"agent-compose/pkg/agentcompose/workspaces"
	driverpkg "agent-compose/pkg/driver"

	"github.com/google/uuid"
)

func (s *Service) ensureProjectRunSession(ctx context.Context, run ProjectRunRecord, prepared runs.Preparation, requestedSessionID string) (runs.SessionResult, error) {
	if s == nil || s.config == nil || s.store == nil || s.driver == nil {
		return runs.SessionResult{}, fmt.Errorf("session runtime dependencies are required")
	}
	tags := runs.SessionTags(run)
	capabilityVars, capabilityTags := capabilities.BuildGatewaySessionVars(capabilities.ProxyTarget(s.cap), prepared.CapsetIDs)
	tags = append(tags, capabilityTags...)
	if sessionID := strings.TrimSpace(requestedSessionID); sessionID != "" {
		session, err := s.store.GetSession(ctx, sessionID)
		if err != nil {
			return runs.SessionResult{}, fmt.Errorf("load session %s: %w", sessionID, err)
		}
		if session.Summary.VMStatus != domain.VMStatusRunning {
			driver, err := driverpkg.ResolveSessionRuntimeDriver(session.Summary.Driver, s.config.RuntimeDriver)
			if err != nil {
				return runs.SessionResult{}, err
			}
			guestImage := driverpkg.ResolveSessionGuestImage(session.Summary.GuestImage, driverpkg.DefaultGuestImageForDriver(s.config, driver))
			if err := images.EnsureDriverImage(ctx, s.config, s.images, images.EnsureRequest{
				Driver:      driver,
				ImageRef:    guestImage,
				ProjectName: run.ProjectName,
				AgentName:   run.AgentName,
			}); err != nil {
				return runs.SessionResult{Session: session}, err
			}
		}
		session.EnvItems = domain.MergeEnvItems(session.EnvItems, capabilityVars)
		session.Summary.Tags = runs.MergeSessionTags(session.Summary.Tags, tags)
		if err := s.startProjectRunSession(ctx, session, "session.resumed", "session resumed for project run"); err != nil {
			return runs.SessionResult{Session: session}, err
		}
		return runs.SessionResult{Session: session}, nil
	}

	workspaceID := ""
	if prepared.Workspace != nil {
		workspaceID = strings.TrimSpace(prepared.Workspace.ID)
	}
	driver, err := driverpkg.ResolveSessionRuntimeDriver(run.Driver, s.config.RuntimeDriver)
	if err != nil {
		return runs.SessionResult{}, err
	}
	guestImage := driverpkg.ResolveSessionGuestImage(run.ImageRef, driverpkg.DefaultGuestImageForDriver(s.config, driver))
	if err := images.EnsureDriverImage(ctx, s.config, s.images, images.EnsureRequest{
		Driver:      driver,
		ImageRef:    guestImage,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
	}); err != nil {
		return runs.SessionResult{}, err
	}
	session, err := s.store.CreateSession(ctx,
		runs.SessionTitle(run),
		"",
		driver,
		guestImage,
		workspaceID,
		domain.SessionTypeManual,
		prepared.Workspace,
		domain.MergeEnvItems(prepared.EnvItems, capabilityVars),
		tags,
	)
	if err != nil {
		return runs.SessionResult{}, err
	}
	session.ProviderEnvItems = prepared.ProviderEnvItems
	if err := s.startProjectRunSession(ctx, session, "session.created", "session started for project run"); err != nil {
		return runs.SessionResult{Session: session, Created: true}, err
	}
	return runs.SessionResult{Session: session, Created: true}, nil
}

func (s *Service) startProjectRunSession(ctx context.Context, session *Session, eventType, eventMessage string) error {
	if session == nil {
		return fmt.Errorf("session is required")
	}
	if err := workspaces.PrepareSessionWorkspace(ctx, s.config, s.configDB, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = s.store.UpdateSession(ctx, session)
		return err
	}
	writeCapabilityGuide(ctx, s.cap, s.store, s.streams, session, capabilities.SessionCapsets(session))
	if session.Summary.VMStatus != domain.VMStatusRunning {
		if err := s.driver.StartSessionVM(ctx, session); err != nil {
			session.Summary.VMStatus = domain.VMStatusFailed
			_ = s.store.UpdateSession(ctx, session)
			return err
		}
	}
	session.Summary.VMStatus = domain.VMStatusRunning
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
		s.bus.Publish(domain.LoaderTopicEvent{
			Topic:     topic,
			Payload:   loaders.SessionTopicPayload(session, "project-run"),
			CreatedAt: time.Now().UTC(),
		})
	}
}
