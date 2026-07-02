package driver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEnsureDockerImageEmptyPolicyPassedThrough(t *testing.T) {
	var gotPolicy string
	resolver := dockerFirstRuntimeImageResolver{
		ensureDocker: func(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (string, error) {
			gotPolicy = pullPolicy
			return imageRef, nil
		},
	}
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	session.Summary.PullPolicy = ""

	_, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, "", 10*time.Minute, resolver)
	if err != nil {
		t.Fatalf("empty pullPolicy returned error: %v", err)
	}
	if gotPolicy != "" {
		t.Fatalf("expected empty pullPolicy to be passed as empty, got %q", gotPolicy)
	}
}

func TestEnsureDockerImageAlwaysPullPolicyPassedThrough(t *testing.T) {
	var gotPolicy string
	resolver := dockerFirstRuntimeImageResolver{
		ensureDocker: func(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (string, error) {
			gotPolicy = pullPolicy
			return imageRef + "@sha256:updated", nil
		},
	}
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	session.Summary.PullPolicy = "always"

	state, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, "always", 10*time.Minute, resolver)
	if err != nil {
		t.Fatalf("always pullPolicy returned error: %v", err)
	}
	if gotPolicy != "always" {
		t.Fatalf("expected pullPolicy=always to be passed through, got %q", gotPolicy)
	}
	if state.Image == "" {
		t.Fatalf("expected resolved image, got empty")
	}
}

func TestEnsureDockerImageNeverPullPolicyPassedThrough(t *testing.T) {
	var gotPolicy string
	resolver := dockerFirstRuntimeImageResolver{
		ensureDocker: func(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (string, error) {
			gotPolicy = pullPolicy
			return imageRef, nil
		},
	}
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	session.Summary.PullPolicy = "never"

	_, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, "never", 10*time.Minute, resolver)
	if err != nil {
		t.Fatalf("never pullPolicy returned error: %v", err)
	}
	if gotPolicy != "never" {
		t.Fatalf("expected pullPolicy=never to be passed through, got %q", gotPolicy)
	}
}

func TestEnsureDockerImageAlwaysFallsBackToLocalOnPullFailure(t *testing.T) {
	pullErr := errors.New("registry unavailable")
	resolver := dockerFirstRuntimeImageResolver{
		ensureDocker: func(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (string, error) {
			return "", pullErr
		},
	}
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)
	session.Summary.PullPolicy = "always"

	_, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, "always", 10*time.Minute, resolver)
	if err == nil {
		t.Fatalf("expected error when ensure returns error, got nil")
	}
	if !errors.Is(err, pullErr) {
		t.Fatalf("expected pullErr in error chain, got: %v", err)
	}
}

func TestPullPolicyTimeoutPassedThrough(t *testing.T) {
	var gotTimeout time.Duration
	resolver := dockerFirstRuntimeImageResolver{
		ensureDocker: func(ctx context.Context, imageRef string, pullPolicy string, pullTimeout time.Duration) (string, error) {
			gotTimeout = pullTimeout
			return imageRef, nil
		},
	}
	root := t.TempDir()
	config := testPrepareSessionStartConfig(root)
	session := testRuntimeMountSession(root)

	wantTimeout := 5 * time.Minute
	_, err := prepareSessionStartWithResolver(context.Background(), config, RuntimeDriverDocker, session, VMState{}, "", wantTimeout, resolver)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTimeout != wantTimeout {
		t.Fatalf("pullTimeout = %v, want %v", gotTimeout, wantTimeout)
	}
}

