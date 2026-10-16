package runs

import (
	"context"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/compose"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SandboxRuntimePreflight is an optional capability of the configured sandbox
// driver: it reports whether the runtime a new sandbox would use can be used at
// all.
//
// The controller asks it before it creates the sandbox record, so a run against
// an unusable runtime - a cluster with no readable kubeconfig, for example - is
// rejected while there is still nothing to clean up, instead of leaving a
// failed sandbox for the removal path and the run's completion retry to chase.
//
// The sandbox it receives is never persisted: it carries only what the
// preflight needs to describe what it checked.
type SandboxRuntimePreflight interface {
	PreflightSandboxRuntime(context.Context, *domain.Sandbox, domain.VMState) error
}

// preflightSandboxRuntime applies the capability to one run's would-be sandbox.
// A driver without the capability has nothing to check and is not delayed.
func (c *Controller) preflightSandboxRuntime(ctx context.Context, run domain.ProjectRunRecord, driver, guestImage string, driverK8s *compose.K8sDriverSpec) error {
	preflight, ok := c.driver.(SandboxRuntimePreflight)
	if !ok {
		return nil
	}
	vmState := domain.VMState{Driver: driver, Image: guestImage}
	if driverK8s != nil {
		vmState.K8sContext = driverK8s.Context
		vmState.K8sNamespace = driverK8s.Namespace
	}
	uncreated := &domain.Sandbox{Summary: domain.SandboxSummary{Driver: driver, GuestImage: guestImage}}
	if err := preflight.PreflightSandboxRuntime(ctx, uncreated, vmState); err != nil {
		return fmt.Errorf("sandbox runtime is not usable for run %s: %w", run.RunID, err)
	}
	return nil
}
