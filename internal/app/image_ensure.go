package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"

	driverpkg "agent-compose/pkg/driver"
)

type driverImageEnsureRequest struct {
	Driver      string
	ImageRef    string
	ProjectName string
	AgentName   string
}

func (s *Service) ensureProjectAgentImages(ctx context.Context, projectName string, agents []ProjectAgentRecord) error {
	if s == nil || s.config == nil {
		return fmt.Errorf("image ensure config is required")
	}
	for _, agent := range agents {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(agent.Driver, s.config.RuntimeDriver)
		if err != nil {
			return fmt.Errorf("ensure image for project %s agent %s: %w", projectName, agent.AgentName, err)
		}
		imageRef := driverpkg.ResolveSessionGuestImage(agent.Image, driverpkg.DefaultGuestImageForDriver(s.config, driver))
		if err := s.ensureDriverImage(ctx, driverImageEnsureRequest{
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

func (s *Service) ensureDriverImage(ctx context.Context, req driverImageEnsureRequest) error {
	if s == nil || s.config == nil {
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
	if s == nil || s.images == nil {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: image backend is required", req.ProjectName, req.AgentName, driver, imageRef)
	}
	if _, err := s.images.InspectImage(ctx, ImageInspectRequest{ImageRef: imageRef}); err == nil {
		return nil
	} else if !imageBackendErrorIsNotFound(err) {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w", req.ProjectName, req.AgentName, driver, imageRef, err)
	}
	if _, err := s.images.PullImage(ctx, ImagePullRequest{ImageRef: imageRef}); err != nil {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w", req.ProjectName, req.AgentName, driver, imageRef, err)
	}
	return nil
}

func imageBackendErrorIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var backendErr imageBackendOpError
	if errors.As(err, &backendErr) {
		return cerrdefs.IsNotFound(backendErr.Err)
	}
	return cerrdefs.IsNotFound(err)
}
