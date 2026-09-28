package runs

import (
	"errors"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestReusingSandboxUnderAnotherIdentityWaitsForUnfinishedRun(t *testing.T) {
	fixture := newControllerRunFixture(t)
	indexer := &recordingCapabilitySandboxIndexer{}
	fixture.controller.capTokens = indexer
	newRun := func(runID string) domain.ProjectRunRecord {
		return persistControllerFixtureRun(t, fixture, domain.ProjectRunRecord{
			RunID: runID, ProjectID: "project-1", ProjectName: "Project", AgentID: "agent-1", AgentName: "worker",
			Driver: driverpkg.RuntimeDriverDocker, ImageRef: "guest:latest",
		})
	}
	runA := newRun("run-a")
	tags := append(SandboxTags(runA), domain.SandboxTag{Name: capabilities.CapsetTagName, Value: "dev"})
	sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "shared", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "sandbox-token", Secret: true}}, tags)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	reuse := func(run domain.ProjectRunRecord, headers []domain.TrustedHeader) error {
		t.Helper()
		ctx := domain.NewContextWithTrustedHeaders(fixture.ctx, headers)
		_, err := fixture.controller.ensureProjectRunSandbox(ctx, run, Preparation{}, RunAgentRequest{SandboxID: sandbox.Summary.ID})
		return err
	}
	userA := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-a"}}
	userB := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-b"}}

	if err := reuse(runA, userA); err != nil {
		t.Fatalf("run A reuse: %v", err)
	}
	coordinator := NewCoordinator(fixture.configDB, nil)
	if _, err := coordinator.MarkRunning(fixture.ctx, runA.RunID, sandbox.Summary.ID); err != nil {
		t.Fatal(err)
	}

	if err := reuse(newRun("run-b"), userB); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("reuse under another identity while run A is running: err = %v, want failed precondition", err)
	}
	if !reflect.DeepEqual(indexer.trustedHeaders, [][]domain.TrustedHeader{userA}) {
		t.Fatalf("rejected reuse rebound the sandbox: %#v", indexer.trustedHeaders)
	}
	if err := reuse(newRun("run-anonymous"), nil); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("reuse without trusted headers while run A is running: err = %v, want failed precondition", err)
	}

	if err := reuse(newRun("run-a2"), userA); err != nil {
		t.Fatalf("reuse under the same identity while run A is running: %v", err)
	}

	if _, err := coordinator.MarkSucceeded(fixture.ctx, TransitionRequest{RunID: runA.RunID}); err != nil {
		t.Fatal(err)
	}
	// run-a2 was only prepared and still holds the sandbox until it ends.
	if _, err := coordinator.MarkFailed(fixture.ctx, TransitionRequest{RunID: "run-a2"}); err != nil {
		t.Fatal(err)
	}
	if err := reuse(newRun("run-b2"), userB); err != nil {
		t.Fatalf("reuse under another identity after the other runs finished: %v", err)
	}
	if got := indexer.trustedHeaders[len(indexer.trustedHeaders)-1]; !reflect.DeepEqual(got, userB) {
		t.Fatalf("sandbox bound to %#v after reuse, want %#v", got, userB)
	}
}
