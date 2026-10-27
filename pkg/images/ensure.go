package images

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Ensure failures carry these sentinels so an RPC boundary can name the
// failure category without parsing text. They are wrapped with %w and remain
// inspectable through errors.Is even after further wrapping.
var (
	ErrImageNotFound    = errors.New("image not found")
	ErrPullFailed       = errors.New("image pull failed")
	ErrInspectFailed    = errors.New("image inspect failed")
	ErrInvalidReference = errors.New("image reference invalid")
)

type EnsureRequest struct {
	Driver      string
	ImageRef    string
	ProjectName string
	AgentName   string
}

// ProjectAgentImagesRequest identifies the project and agents to ensure
// driver images for.
type ProjectAgentImagesRequest struct {
	ProjectName string
	Agents      []domain.ProjectAgentRecord
}

func EnsureProjectAgentImages(ctx context.Context, config *appconfig.Config, backend Backend, req ProjectAgentImagesRequest) error {
	if config == nil {
		return fmt.Errorf("image ensure config is required")
	}
	for _, agent := range req.Agents {
		driver, err := driverpkg.ResolveSandboxRuntimeDriver(agent.Driver, config.RuntimeDriver)
		if err != nil {
			return fmt.Errorf("ensure image for project %s agent %s: %w", req.ProjectName, agent.AgentName, err)
		}
		imageRef := driverpkg.ResolveSandboxGuestImage(agent.Image, driverpkg.DefaultGuestImageForDriver(config, driver))
		if err := EnsureDriverImage(ctx, config, backend, EnsureRequest{
			Driver:      driver,
			ImageRef:    imageRef,
			ProjectName: req.ProjectName,
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
	if autoBackend, ok := backend.(*AutoBackend); ok {
		if autoBackend == nil || autoBackend.docker == nil {
			return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: docker image backend is required", req.ProjectName, req.AgentName, driver, imageRef)
		}
		backend = autoBackend.docker
	}
	if _, err := backend.InspectImage(ctx, InspectRequest{ImageRef: imageRef}); err == nil {
		return nil
	} else if !IsNotFound(err) {
		if isInvalidReference(err) {
			return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w: %w", req.ProjectName, req.AgentName, driver, imageRef, ErrInvalidReference, err)
		}
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w: %w", req.ProjectName, req.AgentName, driver, imageRef, ErrInspectFailed, err)
	}
	if _, err := backend.PullImage(ctx, PullRequest{ImageRef: imageRef}); err != nil {
		if IsNotFound(err) {
			return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w: %w", req.ProjectName, req.AgentName, driver, imageRef, ErrImageNotFound, err)
		}
		if isInvalidReference(err) {
			return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w: %w", req.ProjectName, req.AgentName, driver, imageRef, ErrInvalidReference, err)
		}
		return fmt.Errorf("ensure image for project %s agent %s: driver %s image %s: %w: %w", req.ProjectName, req.AgentName, driver, imageRef, ErrPullFailed, err)
	}
	return nil
}

// isInvalidReference reports whether the backend rejected the reference
// itself, which is distinct from the image being absent.
func isInvalidReference(err error) bool {
	_, kind, ok := ClassifyBackendError(err)
	return ok && kind == ErrorKindInvalidReference
}
