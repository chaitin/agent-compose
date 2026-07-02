package driversession

import (
	appconfig "agent-compose/pkg/config"
	dockerdriver "agent-compose/pkg/driver/docker"
	driverimage "agent-compose/pkg/driver/image"
	drivermount "agent-compose/pkg/driver/mount"
	. "agent-compose/pkg/driver/types"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

func PrepareSessionStart(ctx context.Context, config *appconfig.Config, driver string, session *Session, vmState VMState) (VMState, error) {
	return prepareSessionStartWithResolver(ctx, config, driver, session, vmState, dockerFirstRuntimeImageResolver{ensureDocker: dockerdriver.EnsureDockerImage})
}

func prepareSessionStartWithResolver(ctx context.Context, config *appconfig.Config, driver string, session *Session, vmState VMState, resolver runtimeImageResolver) (VMState, error) {
	if _, err := drivermount.PrepareRuntimeMountManifest(config, session, driver); err != nil {
		return vmState, err
	}
	vmState.Image = driverimage.ResolveSessionGuestImage(vmState.Image, session.Summary.GuestImage, DefaultGuestImageForDriver(config, driver))
	switch driver {
	case RuntimeDriverBoxlite:
		if err := ensureRuntimeAssets(config.BoxRootfsPath); err != nil {
			return vmState, err
		}
		vmState.Registry = config.ImageRegistry
		if vmState.Image != "" {
			slog.Info("agent-compose resolving boxlite guest image", "session_id", session.Summary.ID, "guest_image", vmState.Image)
			resolvedImage, err := resolver.ResolvePrepareImage(ctx, config, driver, vmState.Image)
			if err != nil {
				return vmState, err
			}
			vmState.Image = driverimage.ResolveSessionGuestImage(resolvedImage, vmState.Image)
		}
	case RuntimeDriverDocker:
		vmState.Registry = ""
		if vmState.Image != "" {
			slog.Info("agent-compose ensuring docker guest image", "session_id", session.Summary.ID, "guest_image", vmState.Image)
			resolvedImage, err := resolver.ResolvePrepareImage(ctx, config, driver, vmState.Image)
			if err != nil {
				return vmState, err
			}
			vmState.Image = driverimage.ResolveSessionGuestImage(resolvedImage, vmState.Image)
		}
	case RuntimeDriverMicrosandbox:
		vmState.Registry = ""
	default:
		return vmState, fmt.Errorf("unsupported agent-compose runtime driver %q", driver)
	}
	return vmState, nil
}

type runtimeImageResolver interface {
	ResolvePrepareImage(context.Context, *appconfig.Config, string, string) (string, error)
}

type dockerFirstRuntimeImageResolver struct {
	ensureDocker func(context.Context, string) (string, error)
}

func (r dockerFirstRuntimeImageResolver) ResolvePrepareImage(ctx context.Context, config *appconfig.Config, driver, imageRef string) (string, error) {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return "", nil
	}
	switch driver {
	case RuntimeDriverDocker:
		ensure := r.ensureDocker
		if ensure == nil {
			ensure = dockerdriver.EnsureDockerImage
		}
		return ensure(ctx, imageRef)
	case RuntimeDriverBoxlite, RuntimeDriverMicrosandbox:
		return imageRef, nil
	default:
		return "", fmt.Errorf("unsupported agent-compose runtime driver %q", driver)
	}
}

func ensureRuntimeAssets(rootfs string) error {
	if strings.TrimSpace(rootfs) == "" {
		return nil
	}
	info, err := os.Stat(rootfs)
	if err != nil {
		return fmt.Errorf("agent-compose rootfs path missing %s: verify BOX_ROOTFS_PATH or switch to DEFAULT_IMAGE: %w", rootfs, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agent-compose rootfs path is not a directory: %s", rootfs)
	}
	return nil
}
