package runs

import (
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// TestRunProjectAgentRejectsAnUnusableRuntimeBeforeCreatingASandbox pins the
// stronger layer of the fail-fast rule: when the driver can already tell that
// the runtime is unusable, the run must fail before this controller creates the
// sandbox record. Creating it first is what left the test round with a failed
// sandbox to clean up - and, with the runtime permanently unreachable, with a
// cleanup that could never converge.
func TestRunProjectAgentRejectsAnUnusableRuntimeBeforeCreatingASandbox(t *testing.T) {
	fixture := newControllerRunFixture(t)
	fixture.configDB.revision.SpecJSON = `{"agents":[{"name":"worker","provider":"codex","driver":{"k8s":{"context":"cluster-a","namespace":"namespace-a"}}}]}`
	fixture.driver.preflightErr = domain.ClassifyError(domain.ErrFailedPrecondition, "kubernetes runtime is not usable", errors.New("no kubeconfig"))

	run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
	if err != nil {
		t.Fatalf("RunProjectAgent returned error: %v", err)
	}
	if !errors.Is(execErr, domain.ErrFailedPrecondition) {
		t.Fatalf("RunProjectAgent execution error = %v, want the preflight's failed precondition", execErr)
	}
	if run.SandboxID != "" {
		t.Fatalf("run %#v is bound to sandbox %q, want no sandbox for a refused runtime", run.RunID, run.SandboxID)
	}
	result, listErr := fixture.store.ListSandboxes(fixture.ctx, domain.SandboxListOptions{})
	if listErr != nil {
		t.Fatalf("ListSandboxes returned error: %v", listErr)
	}
	if len(result.Sandboxes) != 0 {
		t.Fatalf("created %d sandbox records for a refused runtime, want none", len(result.Sandboxes))
	}
	if fixture.driver.started {
		t.Fatal("started a sandbox VM after the preflight refused the runtime")
	}
	if fixture.driver.preflightCalls != 1 {
		t.Fatalf("preflight calls = %d, want exactly 1", fixture.driver.preflightCalls)
	}
	if fixture.driver.preflightState.Driver != driver.RuntimeDriverDocker {
		t.Fatalf("preflight driver = %q, want %q", fixture.driver.preflightState.Driver, driver.RuntimeDriverDocker)
	}
	if fixture.driver.preflightState.K8sContext != "cluster-a" || fixture.driver.preflightState.K8sNamespace != "namespace-a" {
		t.Fatalf("preflight state = %#v, want the agent's driver.k8s context and namespace", fixture.driver.preflightState)
	}
}

// TestRunProjectAgentStartsNormallyWhenTheRuntimePreflightAccepts keeps the
// rejection path honest: the preflight runs on every run, so accepting must not
// change what a healthy run does.
func TestRunProjectAgentStartsNormallyWhenTheRuntimePreflightAccepts(t *testing.T) {
	fixture := newControllerRunFixture(t)

	run, execErr, err := runAgentWithRemoveOnCompletion(t, fixture, nil)
	if err != nil || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v run=%#v", err, execErr, run)
	}
	if run.Status != domain.ProjectRunStatusSucceeded || run.SandboxID == "" {
		t.Fatalf("run = %#v, want a succeeded run with a sandbox", run)
	}
	if fixture.driver.preflightCalls != 1 || !fixture.driver.started {
		t.Fatalf("preflight calls = %d, started = %v, want the preflight to run once before the start", fixture.driver.preflightCalls, fixture.driver.started)
	}
}
