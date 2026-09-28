package runs

import (
	"errors"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
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

func TestStickySchedulerReuseUnderAnotherIdentityWaitsForUnfinishedRun(t *testing.T) {
	fixture := newControllerRunFixture(t)
	indexer := &recordingCapabilitySandboxIndexer{}
	fixture.controller.capTokens = indexer
	runSticky := func(headers []domain.TrustedHeader, requestID string) (domain.ProjectRunRecord, error) {
		t.Helper()
		run, execErr, err := fixture.controller.RunProjectAgent(domain.NewContextWithTrustedHeaders(fixture.ctx, headers), RunAgentRequest{
			ProjectID:                "project-1",
			AgentName:                "worker",
			Prompt:                   "do sticky work",
			Source:                   domain.ProjectRunSourceScheduler,
			SchedulerID:              "scheduler-1",
			TriggerID:                "trigger-a",
			ClientRequestID:          requestID,
			CleanupPolicy:            agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
			StickyBindingSchedulerID: "scheduler-1",
			StickyBindingTriggerID:   "trigger-a",
		}, nil)
		return run, errors.Join(err, execErr)
	}
	userA := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-a"}}
	userB := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-b"}}

	first, err := runSticky(userA, "sticky-1")
	if err != nil || first.SandboxID == "" {
		t.Fatalf("first sticky run=%#v err=%v", first, err)
	}
	// Another run, for example a manual one pointed at the sticky sandbox, is
	// still using it.
	persistControllerFixtureRun(t, fixture, domain.ProjectRunRecord{
		RunID: "holder", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		SandboxID: first.SandboxID, Status: domain.ProjectRunStatusRunning,
	})

	if _, err := runSticky(userB, "sticky-2"); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("sticky reuse under another identity: err = %v, want failed precondition", err)
	}
	if got := indexer.trustedHeaders[len(indexer.trustedHeaders)-1]; !reflect.DeepEqual(got, userA) {
		t.Fatalf("rejected sticky reuse rebound the sandbox to %#v", got)
	}
	same, err := runSticky(userA, "sticky-3")
	if err != nil || same.SandboxID != first.SandboxID {
		t.Fatalf("sticky reuse under the same identity run=%#v err=%v", same, err)
	}
}
