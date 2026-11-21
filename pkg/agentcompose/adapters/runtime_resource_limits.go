package adapters

import (
	"context"
	"errors"

	"github.com/chaitin/agent-compose/pkg/compose"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SandboxResourceLimitsResolver resolves the policy.resources block a
// sandbox's project agent declared, or nil when it declared none.
type SandboxResourceLimitsResolver interface {
	ResolveResourceLimits(context.Context, *domain.Sandbox) (*compose.NormalizedSandboxResourcesSpec, error)
}

// WithSandboxResourceLimits attaches the resolver that reads each sandbox's
// declared resource limit. Without it no declaration is visible, so every
// sandbox keeps the undeclared sizing.
func WithSandboxResourceLimits(resolver SandboxResourceLimitsResolver) RuntimeProviderOption {
	return func(provider *runtimeProvider) { provider.resourceLimits.declarations = resolver }
}

// driverSandboxResourceLimits turns an agent's declaration into the
// driver-boundary limit. It is the single gate for the declaration: a declared
// limit is a hard requirement, so a limit on a driver that does not enforce
// resource_limits refuses the start here, before any runtime state exists. The
// declaration wins over the microVM defaults (SANDBOX_CPUS, SANDBOX_MEMORY_MIB)
// in both directions; there is no deployment ceiling on it. Its zero value is
// inert.
type driverSandboxResourceLimits struct {
	declarations SandboxResourceLimitsResolver
	// driver is the runtime this gate's adapter drives. Each adapter is built
	// for one driver, so the capability check never depends on a sandbox's
	// possibly empty driver field.
	driver string
}

func (d driverSandboxResourceLimits) forDriver(driver string) driverSandboxResourceLimits {
	d.driver = driver
	return d
}

func (d driverSandboxResourceLimits) resolve(ctx context.Context, session *domain.Sandbox) (*driverpkg.SandboxResourceLimits, error) {
	if d.declarations == nil || session == nil {
		return nil, nil
	}
	declared, err := d.declarations.ResolveResourceLimits(ctx, session)
	if err != nil {
		return nil, err
	}
	if declared == nil {
		return nil, nil
	}
	limits := &driverpkg.SandboxResourceLimits{CPUMillis: declared.CPUMillis, MemoryMiB: declared.MemoryMiB}
	if !limits.Declared() {
		return nil, nil
	}
	if err := driverpkg.RequireSandboxResourceLimits(d.driver, limits); err != nil {
		return nil, classifyResourceLimitsError(err)
	}
	return limits, nil
}

// classifyResourceLimitsError marks a declared limit the runtime could not
// apply as a failed precondition: the request is valid, the deployment cannot
// satisfy it, and retrying unchanged will not help.
func classifyResourceLimitsError(err error) error {
	if err == nil || !errors.Is(err, driverpkg.ErrSandboxResourceLimitsUnavailable) || errors.Is(err, domain.ErrFailedPrecondition) {
		return err
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, "declared sandbox resource limits cannot be enforced", err)
}

// classifyStorageRootError marks a driver storage root other accounts could
// substitute as a failed precondition: the deployment must fix the directory
// permissions, and retrying unchanged will not help. It applies to every
// runtime operation because the driver refuses each of them, stop and remove
// included, until the permissions are fixed.
func classifyStorageRootError(err error) error {
	if err == nil || !errors.Is(err, driverpkg.ErrSandboxStorageRootUnsafe) || errors.Is(err, domain.ErrFailedPrecondition) {
		return err
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox storage root is not protected from other accounts", err)
}
