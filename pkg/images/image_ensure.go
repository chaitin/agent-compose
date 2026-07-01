package images

import (
	"context"
	"fmt"
	"strings"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
)

type DriverImageEnsureRequest struct {
	Driver      string
	ImageRef    string
	ProjectName string
	AgentName   string
}

func EnsureDriverImage(ctx context.Context, config *appconfig.Config, backend ImageBackend, req DriverImageEnsureRequest) error {
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
	if _, err := backend.InspectImage(ctx, ImageInspectRequest{ImageRef: imageRef}); err == nil {
		return nil
	} else if !BackendErrorIsNotFound(err) {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w", req.ProjectName, req.AgentName, driver, imageRef, err)
	}
	if _, err := backend.PullImage(ctx, ImagePullRequest{ImageRef: imageRef}); err != nil {
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w", req.ProjectName, req.AgentName, driver, imageRef, err)
	}
	return nil
}
