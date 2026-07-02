package driver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	appconfig "agent-compose/internal/config"
)

func TestDockerFirstRuntimeImageResolverSkipsDockerForNonDockerPrepare(t *testing.T) {
	called := false
	resolver := dockerFirstRuntimeImageResolver{ensureDocker: func(ctx context.Context, imageRef string) (string, error) {
		called = true
		return "", errors.New("docker unavailable")
	}}
	for _, driver := range []string{RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		resolved, err := resolver.ResolvePrepareImage(context.Background(), &appconfig.Config{}, driver, "guest:latest")
		if err != nil {
			t.Fatalf("ResolvePrepareImage(%s) returned error: %v", driver, err)
		}
		if resolved != "guest:latest" {
			t.Fatalf("ResolvePrepareImage(%s) = %q", driver, resolved)
		}
	}
	if called {
		t.Fatalf("Docker ensure was called for non-Docker prepare")
	}
}

func TestPrepareSessionStartBoxLiteDoesNotFailWhenDockerUnavailable(t *testing.T) {
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	resolver := dockerFirstRuntimeImageResolver{ensureDocker: func(ctx context.Context, imageRef string) (string, error) {
		return "", errors.New("docker unavailable")
	}}

	state, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverBoxlite, session, VMState{}, resolver)
	if err != nil {
		t.Fatalf("PrepareSessionStart boxlite returned error: %v", err)
	}
	if state.Image != config.DefaultImage || state.Registry != config.ImageRegistry {
		t.Fatalf("boxlite state = %#v", state)
	}
	if _, err := loadDirectoryRuntimeMountManifest(session, RuntimeDriverBoxlite); err != nil {
		t.Fatalf("boxlite mount manifest was not written: %v", err)
	}
}

func TestPrepareSessionStartDockerStillRequiresDockerEnsure(t *testing.T) {
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	wantErr := errors.New("docker unavailable")
	resolver := dockerFirstRuntimeImageResolver{ensureDocker: func(ctx context.Context, imageRef string) (string, error) {
		if imageRef != config.DockerDefaultImage {
			t.Fatalf("ensure imageRef = %q, want %q", imageRef, config.DockerDefaultImage)
		}
		return "", wantErr
	}}

	_, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, resolver)
	if !errors.Is(err, wantErr) {
		t.Fatalf("PrepareSessionStart docker error = %v, want %v", err, wantErr)
	}
}

func TestPrepareSessionStartDockerUsesResolvedImage(t *testing.T) {
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	resolver := dockerFirstRuntimeImageResolver{ensureDocker: func(ctx context.Context, imageRef string) (string, error) {
		return "guest@sha256:resolved", nil
	}}

	state, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, resolver)
	if err != nil {
		t.Fatalf("PrepareSessionStart docker returned error: %v", err)
	}
	if state.Image != "guest@sha256:resolved" || state.Registry != "" {
		t.Fatalf("docker state = %#v", state)
	}
}

func testPrepareSessionStartConfig(root string) *appconfig.Config {
	config := testRuntimeMountConfig()
	config.DataRoot = root
	config.SessionRoot = filepath.Join(root, "sessions")
	config.DefaultImage = "boxlite-guest:latest"
	config.DockerDefaultImage = "docker-guest:latest"
	config.ImageRegistry = "registry.example"
	return config
}
