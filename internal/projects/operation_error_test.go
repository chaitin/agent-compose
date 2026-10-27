package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
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
