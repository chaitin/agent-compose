package runs

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type recordingCapabilitySandboxIndexer struct {
	indexed        []*domain.Sandbox
	trustedHeaders [][]domain.TrustedHeader
	revoked        []string
}

func (i *recordingCapabilitySandboxIndexer) IndexSandbox(sandbox *domain.Sandbox, headers []domain.TrustedHeader) {
	i.indexed = append(i.indexed, sandbox)
	i.trustedHeaders = append(i.trustedHeaders, append([]domain.TrustedHeader(nil), headers...))
}

func (i *recordingCapabilitySandboxIndexer) TrustedHeadersConflict(sandboxID string, headers []domain.TrustedHeader) bool {
	if len(i.indexed) == 0 || i.indexed[len(i.indexed)-1].Summary.ID != sandboxID {
		return false
	}
	return !reflect.DeepEqual(i.trustedHeaders[len(i.trustedHeaders)-1], append([]domain.TrustedHeader(nil), headers...))
}

func (i *recordingCapabilitySandboxIndexer) RevokeSandbox(sandboxID string) {
	i.revoked = append(i.revoked, sandboxID)
}

type projectRunRemovalStub struct {
	result sandboxes.RemovalResult
	err    error
}

func (s projectRunRemovalStub) Remove(context.Context, string, bool) (sandboxes.RemovalResult, error) {
	return s.result, s.err
}

func TestProjectRunCapabilityTokenLifecycle(t *testing.T) {
	t.Run("start and resume index only after running state reload", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		indexer := &recordingCapabilitySandboxIndexer{}
		fixture.controller.capTokens = indexer
		sandbox := newProjectRunCapabilitySandbox(t, fixture, domain.VMStatusStopped)

		if err := fixture.controller.startProjectRunSandbox(fixture.ctx, sandbox, sandboxStartEvent{Type: "sandbox.resumed", Message: "resumed"}, nil); err != nil {
			t.Fatalf("startProjectRunSandbox: %v", err)
		}
		if len(indexer.indexed) != 1 {
			t.Fatalf("indexed count = %d, want 1", len(indexer.indexed))
		}
		indexed := indexer.indexed[0]
		if indexed.Summary.VMStatus != domain.VMStatusRunning || capabilities.SandboxToken(indexed) == "" {
			t.Fatalf("indexed sandbox = %#v", indexed)
		}
		reloaded, err := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID)
		if err != nil {
			t.Fatalf("reload sandbox: %v", err)
		}
		if indexed.Summary.UpdatedAt != reloaded.Summary.UpdatedAt {
			t.Fatalf("indexed UpdatedAt = %v, reloaded = %v", indexed.Summary.UpdatedAt, reloaded.Summary.UpdatedAt)
		}
	})

	t.Run("start failure does not index", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		indexer := &recordingCapabilitySandboxIndexer{}
		fixture.controller.capTokens = indexer
		fixture.driver.startErr = errors.New("start failed")
		sandbox := newProjectRunCapabilitySandbox(t, fixture, domain.VMStatusStopped)

		if err := fixture.controller.startProjectRunSandbox(fixture.ctx, sandbox, sandboxStartEvent{Type: "sandbox.resumed", Message: "resumed"}, nil); !errors.Is(err, fixture.driver.startErr) {
			t.Fatalf("startProjectRunSandbox error = %v", err)
		}
		if len(indexer.indexed) != 0 || len(indexer.revoked) != 0 {
			t.Fatalf("index changes = indexed %d revoked %v", len(indexer.indexed), indexer.revoked)
		}
	})

	t.Run("stop success and already stopped revoke", func(t *testing.T) {
		for _, status := range []string{domain.VMStatusRunning, domain.VMStatusStopped} {
			t.Run(status, func(t *testing.T) {
				fixture := newControllerRunFixture(t)
				indexer := &recordingCapabilitySandboxIndexer{}
				fixture.controller.capTokens = indexer
				sandbox := newProjectRunCapabilitySandbox(t, fixture, status)

				if err := fixture.controller.stopProjectRunSandbox(fixture.ctx, sandbox); err != nil {
					t.Fatalf("stopProjectRunSandbox: %v", err)
				}
				if len(indexer.revoked) != 1 || indexer.revoked[0] != sandbox.Summary.ID {
					t.Fatalf("revoked = %v", indexer.revoked)
				}
			})
		}
	})

	t.Run("stop failure preserves index", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		indexer := &recordingCapabilitySandboxIndexer{}
		fixture.controller.capTokens = indexer
		fixture.driver.stopErr = errors.New("stop failed")
		sandbox := newProjectRunCapabilitySandbox(t, fixture, domain.VMStatusRunning)

		if err := fixture.controller.stopProjectRunSandbox(fixture.ctx, sandbox); !errors.Is(err, fixture.driver.stopErr) {
			t.Fatalf("stopProjectRunSandbox error = %v", err)
		}
		if len(indexer.revoked) != 0 {
			t.Fatalf("revoked = %v", indexer.revoked)
		}
	})

	t.Run("runtime release failure finalizes confirmed stop", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		indexer := &recordingCapabilitySandboxIndexer{}
		fixture.controller.capTokens = indexer
		releaseErr := errors.New("runtime release failed")
		fixture.driver.removeErr = releaseErr
		sandbox := newProjectRunCapabilitySandbox(t, fixture, domain.VMStatusRunning)
		sandbox.StoppedRuntimePolicy = domain.StoppedRuntimePolicyRemove
		if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
			t.Fatalf("UpdateSandbox: %v", err)
		}

		if err := fixture.controller.stopProjectRunSandbox(fixture.ctx, sandbox); !errors.Is(err, releaseErr) {
			t.Fatalf("stopProjectRunSandbox error = %v, want %v", err, releaseErr)
		}
		loaded, err := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID)
		if err != nil {
			t.Fatalf("GetSandbox: %v", err)
		}
		if loaded.Summary.VMStatus != domain.VMStatusStopped || sandboxes.EffectiveStoppedRuntimeState(loaded) != domain.StoppedRuntimeStateReleasePending {
			t.Fatalf("stopped sandbox = %#v release=%#v", loaded.Summary, loaded.StoppedRuntime)
		}
		if len(indexer.revoked) != 1 || indexer.revoked[0] != sandbox.Summary.ID {
			t.Fatalf("revoked = %v, want [%s]", indexer.revoked, sandbox.Summary.ID)
		}
		events, err := fixture.store.ListEvents(fixture.ctx, sandbox.Summary.ID)
		if err != nil {
			t.Fatalf("ListEvents: %v", err)
		}
		if !sandboxEventTypeExists(events, "sandbox.runtime_release_failed") || !sandboxEventTypeExists(events, "sandbox.stopped") {
			t.Fatalf("events = %#v, want release failure and stopped", events)
		}
	})

	t.Run("remove revokes only after confirmed removal", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			result  sandboxes.RemovalResult
			err     error
			wantRev bool
		}{
			{name: "success", result: sandboxes.RemovalResult{Removed: true}, wantRev: true},
			{name: "incomplete", result: sandboxes.RemovalResult{}},
			{name: "failure", err: errors.New("remove failed")},
		} {
			t.Run(tc.name, func(t *testing.T) {
				indexer := &recordingCapabilitySandboxIndexer{}
				controller := NewController(ControllerDependencies{CapTokens: indexer, Removal: projectRunRemovalStub{result: tc.result, err: tc.err}})
				sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}}
				err := controller.cleanupProjectRunSandboxByPolicy(context.Background(), SandboxResult{Sandbox: sandbox, Created: true}, agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION)
				if !errors.Is(err, tc.err) {
					t.Fatalf("cleanup error = %v, want %v", err, tc.err)
				}
				if got := len(indexer.revoked) == 1; got != tc.wantRev {
					t.Fatalf("revoked = %v, wantRev = %v", indexer.revoked, tc.wantRev)
				}
			})
		}
	})

	t.Run("fallback remove revokes exactly once", func(t *testing.T) {
		fixture := newControllerRunFixture(t)
		indexer := &recordingCapabilitySandboxIndexer{}
		fixture.controller.capTokens = indexer
		sandbox := newProjectRunCapabilitySandbox(t, fixture, domain.VMStatusRunning)

		err := fixture.controller.cleanupProjectRunSandboxByPolicy(
			fixture.ctx,
			SandboxResult{Sandbox: sandbox, Created: true},
			agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
		)
		if err != nil {
			t.Fatalf("cleanupProjectRunSandboxByPolicy: %v", err)
		}
		if len(indexer.revoked) != 1 || indexer.revoked[0] != sandbox.Summary.ID {
			t.Fatalf("revoked = %v, want [%s]", indexer.revoked, sandbox.Summary.ID)
		}
		if _, err := fixture.store.GetSandbox(fixture.ctx, sandbox.Summary.ID); err == nil {
			t.Fatal("removed sandbox is still present")
		}
	})
}

func TestProjectRunTrustedHeadersRemainTransientAndClearOnReuse(t *testing.T) {
	fixture := newControllerRunFixture(t)
	indexer := &recordingCapabilitySandboxIndexer{}
	fixture.controller.capTokens = indexer
	run := persistControllerFixtureRun(t, fixture, domain.ProjectRunRecord{
		RunID:       "run-trusted-headers",
		ProjectID:   "project-1",
		ProjectName: "Project",
		AgentID:     "agent-1",
		AgentName:   "worker",
		Driver:      driverpkg.RuntimeDriverDocker,
		ImageRef:    "guest:latest",
	})
	tags := append(SandboxTags(run), domain.SandboxTag{Name: capabilities.CapsetTagName, Value: "dev"})
	sandbox, err := fixture.store.CreateSandbox(
		fixture.ctx,
		"trusted headers",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		"",
		domain.SandboxTypeManual,
		nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "sandbox-token", Secret: true}},
		tags,
	)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatal(err)
	}

	trusted := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-1"}}
	ctx := domain.NewContextWithTrustedHeaders(fixture.ctx, trusted)
	result, err := fixture.controller.ensureProjectRunSandbox(ctx, run, Preparation{}, RunAgentRequest{SandboxID: sandbox.Summary.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(indexer.trustedHeaders) != 1 || !reflect.DeepEqual(indexer.trustedHeaders[0], trusted) {
		t.Fatalf("indexed trusted headers = %#v, want %#v", indexer.trustedHeaders, trusted)
	}
	for _, tag := range result.Sandbox.Summary.Tags {
		if strings.HasPrefix(strings.ToLower(tag.Name), "x-mpi-") {
			t.Fatalf("trusted header persisted as sandbox tag: %#v", tag)
		}
	}
	persisted, err := fixture.store.LoadSandbox(sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range persisted.Summary.Tags {
		if strings.HasPrefix(strings.ToLower(tag.Name), "x-mpi-") {
			t.Fatalf("trusted header persisted in sandbox store: %#v", tag)
		}
	}

	if _, err := fixture.controller.ensureProjectRunSandbox(fixture.ctx, run, Preparation{}, RunAgentRequest{SandboxID: sandbox.Summary.ID}); err != nil {
		t.Fatal(err)
	}
	if len(indexer.trustedHeaders) != 2 || len(indexer.trustedHeaders[1]) != 0 {
		t.Fatalf("reused sandbox retained prior trusted headers: %#v", indexer.trustedHeaders)
	}
}

func TestStartProjectRunPreservesTrustedHeadersForDetachedExecution(t *testing.T) {
	fixture := newControllerRunFixture(t)
	indexer := &recordingCapabilitySandboxIndexer{}
	fixture.controller.capTokens = indexer
	trusted := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-1"}}
	requestCtx := domain.NewContextWithTrustedHeaders(fixture.ctx, trusted)

	started, err := fixture.controller.StartProjectRun(requestCtx, RunAgentRequest{
		ProjectID: "project-1",
		AgentName: "worker",
		Prompt:    "do work",
	})
	if err != nil {
		t.Fatal(err)
	}

	// RunSupervisor.StartRun executes with a daemon-root-derived context so the
	// started run must carry the request metadata across that async boundary.
	execCtx, cancel := context.WithCancelCause(fixture.ctx)
	defer cancel(nil)
	_, _, _ = started.Execute(execCtx, nil)

	if len(indexer.trustedHeaders) == 0 {
		t.Fatal("sandbox was not indexed")
	}
	if !reflect.DeepEqual(indexer.trustedHeaders[0], trusted) {
		t.Fatalf("indexed trusted headers = %#v, want %#v", indexer.trustedHeaders[0], trusted)
	}
}

func sandboxEventTypeExists(events []domain.SandboxEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func newProjectRunCapabilitySandbox(t *testing.T, fixture *controllerRunFixture, status string) *domain.Sandbox {
	t.Helper()
	sandbox, err := fixture.store.CreateSandbox(fixture.ctx, "project run", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", domain.SandboxTypeManual, nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "token-1", Secret: true}},
		[]domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "capset-1"}},
	)
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	sandbox.Summary.VMStatus = status
	if err := fixture.store.UpdateSandbox(fixture.ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox: %v", err)
	}
	return sandbox
}
