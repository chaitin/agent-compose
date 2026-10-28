package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/imagecache"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

type failingSchedulerValidator struct{}

func (failingSchedulerValidator) Validate(context.Context, string, string) (schedulers.SchedulerValidationResult, error) {
	return schedulers.SchedulerValidationResult{}, errors.New("SyntaxError: unexpected token")
}

func (failingSchedulerValidator) Refresh(context.Context) error { return nil }

func normalizedScriptProject(t *testing.T) NormalizedProject {
	t.Helper()
	raw, err := compose.Parse([]byte(`
name: reason-project
workspaces:
  default:
    provider: file
    path: .
agents:
  worker:
    provider: codex
    model: gpt-test
    image: guest:latest
    driver:
      docker: {}
    scheduler:
      script: |
        export default { triggers: [] }
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	spec, err := compose.Normalize(raw, compose.NormalizeOptions{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	hash, err := spec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	return NormalizedProject{Spec: spec, SpecHash: hash, SourcePath: "/repo/agent-compose.yaml"}
}

func TestSchedulerScriptIssueCarriesReasonAndNotApplied(t *testing.T) {
	ctx := context.Background()
	controller := NewController(ControllerDependencies{
		Config:     &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		Store:      &controllerCoverageStore{},
		Schedulers: failingSchedulerValidator{},
	})
	normalized := normalizedScriptProject(t)

	validation, err := controller.ValidateProject(ctx, normalized, nil)
	if err != nil {
		t.Fatalf("ValidateProject returned error: %v", err)
	}
	if validation.Valid {
		t.Fatal("ValidateProject valid = true, want false")
	}
	if len(validation.Issues) != 1 {
		t.Fatalf("ValidateProject issues = %#v, want one", validation.Issues)
	}
	issue := validation.Issues[0]
	if issue.Path != "agents.worker.scheduler.script" {
		t.Fatalf("issue path = %q", issue.Path)
	}
	if issue.Reason != apierror.ReasonSchedulerScriptInvalid {
		t.Fatalf("issue reason = %d, want scheduler_script_invalid", issue.Reason)
	}

	result, err := controller.ApplyProject(ctx, ApplyRequest{Normalized: normalized})
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if result.Applied {
		t.Fatal("ApplyProject applied = true, want false")
	}
	if result.Effect != apierror.EffectNotApplied {
		t.Fatalf("ApplyProject effect = %d, want not_applied", result.Effect)
	}
	if len(result.Issues) != 1 || result.Issues[0].Reason != apierror.ReasonSchedulerScriptInvalid {
		t.Fatalf("ApplyProject issues = %#v", result.Issues)
	}
}

func TestPreWriteApplyFailuresAreMarkedNotApplied(t *testing.T) {
	ctx := context.Background()
	normalized := normalizedScriptProject(t)

	// Request issues fail before the store is even consulted.
	controller := NewController(ControllerDependencies{})
	result, err := controller.ApplyProject(ctx, ApplyRequest{
		Normalized: normalized,
		Issues:     []ValidationIssue{{Path: "spec", Message: "bad"}},
	})
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if result.Effect != apierror.EffectNotApplied {
		t.Fatalf("request-issue effect = %d, want not_applied", result.Effect)
	}

	// A missing store fails before any write.
	_, err = controller.ApplyProject(ctx, ApplyRequest{Normalized: normalized})
	if !apierror.IsNotApplied(err) {
		t.Fatalf("missing store error %v is not marked not-applied", err)
	}
}

type cancellingImagesBackend struct{}

func (cancellingImagesBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (cancellingImagesBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, context.Canceled
}

func (cancellingImagesBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, context.Canceled
}

func (cancellingImagesBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

func TestImageEnsureCancellationIsNotMarkedNotApplied(t *testing.T) {
	ctx := context.Background()
	raw, err := compose.Parse([]byte(`
name: cancel-image
agents:
  worker:
    provider: codex
    image: guest:latest
    driver:
      docker: {}
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	spec, err := compose.Normalize(raw, compose.NormalizeOptions{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	hash, err := spec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	normalized := NormalizedProject{Spec: spec, SpecHash: hash, SourcePath: "/projects/cancel-image"}
	controller := NewController(ControllerDependencies{
		Config:  &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		Store:   &pr551Store{},
		Images:  cancellingImagesBackend{},
		Volumes: pr551NoopVolumeManager{},
	})
	_, err = controller.ApplyProject(ctx, ApplyRequest{Normalized: normalized})
	if err == nil {
		t.Fatal("ApplyProject returned nil error, want image ensure cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error does not wrap context.Canceled: %v", err)
	}
	if apierror.IsNotApplied(err) {
		t.Fatal("image ensure cancellation was marked not-applied, want unknown")
	}
}

// imageNotFoundError returns a backend error the images package classifies as
// not-found, so EnsureDriverImage proceeds from inspect to pull.
func imageNotFoundError(op, ref string) error {
	return images.OpError{
		Op:       op,
		ImageRef: ref,
		Err:      imagecache.NewError(imagecache.ErrorKindNotFound, op, ref, errors.New("missing")),
	}
}

// imageInvalidReferenceError returns a backend error the images package
// classifies as an invalid reference.
func imageInvalidReferenceError(op, ref string) error {
	return images.OpError{
		Op:       op,
		ImageRef: ref,
		Err:      imagecache.NewError(imagecache.ErrorKindInvalidReference, op, ref, errors.New("invalid reference")),
	}
}

// ensureFailureImagesBackend returns canned inspect/pull errors for a single
// agent, so the effect of each image ensure failure category can be tested in
// isolation.
type ensureFailureImagesBackend struct {
	inspectErr error
	pullErr    error
}

func (b ensureFailureImagesBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (b ensureFailureImagesBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, b.inspectErr
}

func (b ensureFailureImagesBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, b.pullErr
}

func (b ensureFailureImagesBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

func TestImageEnsureEffectBoundary(t *testing.T) {
	const singleAgentYAML = `
name: image-effect-boundary
agents:
  worker:
    provider: codex
    image: guest:latest
    driver:
      docker: {}
`
	tests := []struct {
		name     string
		backend  images.Backend
		wantMark bool
	}{
		{
			name: "not found",
			backend: ensureFailureImagesBackend{
				inspectErr: imageNotFoundError("inspect image", "guest:latest"),
				pullErr:    imageNotFoundError("pull image", "guest:latest"),
			},
			wantMark: true,
		},
		{
			name: "invalid reference",
			backend: ensureFailureImagesBackend{
				inspectErr: imageInvalidReferenceError("inspect image", "guest:latest"),
			},
			wantMark: true,
		},
		{
			name: "pull failed",
			backend: ensureFailureImagesBackend{
				inspectErr: imageNotFoundError("inspect image", "guest:latest"),
				pullErr:    errors.New("registry unavailable"),
			},
			wantMark: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			raw, err := compose.Parse([]byte(singleAgentYAML))
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			spec, err := compose.Normalize(raw, compose.NormalizeOptions{ProjectDir: t.TempDir()})
			if err != nil {
				t.Fatalf("Normalize returned error: %v", err)
			}
			hash, err := spec.Hash()
			if err != nil {
				t.Fatalf("Hash returned error: %v", err)
			}
			controller := NewController(ControllerDependencies{
				Config:  &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
				Store:   &pr551Store{},
				Images:  tc.backend,
				Volumes: pr551NoopVolumeManager{},
			})
			_, err = controller.ApplyProject(ctx, ApplyRequest{Normalized: NormalizedProject{Spec: spec, SpecHash: hash, SourcePath: "/projects/image-effect-boundary"}})
			if err == nil {
				t.Fatal("ApplyProject returned nil error, want image ensure failure")
			}
			if got := apierror.IsNotApplied(err); got != tc.wantMark {
				t.Fatalf("ApplyProject error marked not-applied = %v, want %v (err=%v)", got, tc.wantMark, err)
			}
		})
	}
}

// partialPullImagesBackend succeeds the pull for one agent and fails the pull
// for every other agent, modelling a multi-agent apply where the first pull
// mutates the image cache before a later pull fails.
type partialPullImagesBackend struct{}

func (partialPullImagesBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (partialPullImagesBackend) InspectImage(_ context.Context, req images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, imageNotFoundError("inspect image", req.ImageRef)
}

func (partialPullImagesBackend) PullImage(_ context.Context, req images.PullRequest) (images.PullResult, error) {
	if req.ImageRef == "guest:first" {
		return images.PullResult{}, nil
	}
	return images.PullResult{}, errors.New("registry unavailable")
}

func (partialPullImagesBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

func TestImageEnsurePartialPullAcrossAgentsIsUnknown(t *testing.T) {
	ctx := context.Background()
	raw, err := compose.Parse([]byte(`
name: partial-pull
agents:
  first:
    provider: codex
    image: guest:first
    driver:
      docker: {}
  second:
    provider: codex
    image: guest:second
    driver:
      docker: {}
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	spec, err := compose.Normalize(raw, compose.NormalizeOptions{ProjectDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	hash, err := spec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	controller := NewController(ControllerDependencies{
		Config:  &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		Store:   &pr551Store{},
		Images:  partialPullImagesBackend{},
		Volumes: pr551NoopVolumeManager{},
	})
	_, err = controller.ApplyProject(ctx, ApplyRequest{Normalized: NormalizedProject{Spec: spec, SpecHash: hash, SourcePath: "/projects/partial-pull"}})
	if err == nil {
		t.Fatal("ApplyProject returned nil error, want pull failure on the second agent")
	}
	if !errors.Is(err, images.ErrPullFailed) {
		t.Fatalf("error does not wrap images.ErrPullFailed: %v", err)
	}
	if apierror.IsNotApplied(err) {
		t.Fatal("partial pull across agents was marked not-applied, want unknown")
	}
}

// partialPullByCategoryImagesBackend succeeds the first agent's pull and fails
// the second with a canned backend error, so the multi-agent effect can be
// asserted for each failure category.
type partialPullByCategoryImagesBackend struct {
	secondPullErr error
}

func (b partialPullByCategoryImagesBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (b partialPullByCategoryImagesBackend) InspectImage(_ context.Context, req images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, imageNotFoundError("inspect image", req.ImageRef)
}

func (b partialPullByCategoryImagesBackend) PullImage(_ context.Context, req images.PullRequest) (images.PullResult, error) {
	if req.ImageRef == "guest:first" {
		return images.PullResult{}, nil
	}
	return images.PullResult{}, b.secondPullErr
}

func (b partialPullByCategoryImagesBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

// TestImageEnsurePartialPullByCategoryIsUnknown verifies that a not-found or
// invalid-reference failure on a later agent does not claim NOT_APPLIED once an
// earlier agent's image may already have been pulled.
func TestImageEnsurePartialPullByCategoryIsUnknown(t *testing.T) {
	tests := []struct {
		name      string
		secondErr error
		wantWrap  error
	}{
		{
			name:      "second image not found",
			secondErr: imageNotFoundError("pull image", "guest:second"),
			wantWrap:  images.ErrImageNotFound,
		},
		{
			name:      "second image reference invalid",
			secondErr: imageInvalidReferenceError("pull image", "guest:second"),
			wantWrap:  images.ErrInvalidReference,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			raw, err := compose.Parse([]byte(`
name: partial-pull-by-category
agents:
  first:
    provider: codex
    image: guest:first
    driver:
      docker: {}
  second:
    provider: codex
    image: guest:second
    driver:
      docker: {}
`))
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			spec, err := compose.Normalize(raw, compose.NormalizeOptions{ProjectDir: t.TempDir()})
			if err != nil {
				t.Fatalf("Normalize returned error: %v", err)
			}
			hash, err := spec.Hash()
			if err != nil {
				t.Fatalf("Hash returned error: %v", err)
			}
			controller := NewController(ControllerDependencies{
				Config:  &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
				Store:   &pr551Store{},
				Images:  partialPullByCategoryImagesBackend{secondPullErr: tc.secondErr},
				Volumes: pr551NoopVolumeManager{},
			})
			_, err = controller.ApplyProject(ctx, ApplyRequest{Normalized: NormalizedProject{Spec: spec, SpecHash: hash, SourcePath: "/projects/partial-pull-by-category"}})
			if err == nil {
				t.Fatal("ApplyProject returned nil error, want failure on the second agent")
			}
			if !errors.Is(err, tc.wantWrap) {
				t.Fatalf("error does not wrap %v: %v", tc.wantWrap, err)
			}
			if !errors.Is(err, images.ErrImageEnsurePartial) {
				t.Fatalf("error does not wrap images.ErrImageEnsurePartial: %v", err)
			}
			if apierror.IsNotApplied(err) {
				t.Fatal("partial pull across agents was marked not-applied, want unknown")
			}
		})
	}
}
