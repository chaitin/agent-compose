package images

import (
	"agent-compose/pkg/agentcompose/domain"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"fmt"
	"strings"
)

type EnsureRequest struct {
	Driver      string
	ImageRef    string
	ProjectName string
	AgentName   string
}

func EnsureProjectAgentImages(ctx context.Context, config *appconfig.Config, backend Backend, projectName string, agents []domain.ProjectAgentRecord) error {
	if config == nil {
		return fmt.Errorf("image ensure config is required")
	}
	for _, agent := range agents {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(agent.Driver, config.RuntimeDriver)
		if err != nil {
			return fmt.Errorf("ensure image for project %s agent %s: %w", projectName, agent.AgentName, err)
		}
		imageRef := driverpkg.ResolveSessionGuestImage(agent.Image, driverpkg.DefaultGuestImageForDriver(config, driver))
		if err := EnsureDriverImage(ctx, config, backend, EnsureRequest{
			Driver:      driver,
			ImageRef:    imageRef,
			ProjectName: projectName,
			AgentName:   agent.AgentName,
		}); err != nil {
			return err
		}
	}
	return nil
}

func EnsureDriverImage(ctx context.Context, config *appconfig.Config, backend Backend, req EnsureRequest) error {
	if config == nil {
		return fmt.Errorf("image ensure config is required")
	}
	driver := driverpkg.ResolveRuntimeDriver(req.Driver)
	if driver != driverpkg.RuntimeDriverDocker {
		return nil
	}
	imageRef := strings.TrimSpace(req.ImageRef)
	if imageRef == "" {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image is required", req.ProjectName, req.AgentName, driver)
	}
	if backend == nil {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: image backend is required", req.ProjectName, req.AgentName, driver, imageRef)
	}
	if _, err := backend.InspectImage(ctx, InspectRequest{ImageRef: imageRef}); err == nil {
		return nil
	} else if !IsNotFound(err) {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w", req.ProjectName, req.AgentName, driver, imageRef, err)
	}
	if _, err := backend.PullImage(ctx, PullRequest{ImageRef: imageRef}); err != nil {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w", req.ProjectName, req.AgentName, driver, imageRef, err)
	}
	return nil
}
