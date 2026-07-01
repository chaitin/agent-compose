package agentcompose

import (
	"context"
	"fmt"
	"strings"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/images"
)

type driverImageEnsureRequest struct {
	Driver      string
	ImageRef    string
	ProjectName string
	AgentName   string
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
	return images.BackendErrorIsNotFound(err)
}
