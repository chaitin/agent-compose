package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
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
// limit is a hard requirement, so a limit above the deployment's ceiling or on
// a driver that does not enforce resource_limits refuses the start here,
// before any runtime state exists. Its zero value is inert.
type driverSandboxResourceLimits struct {
	config       *appconfig.Config
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
	if d.config != nil {
		if ceiling := d.config.SandboxMaxCPUMillis; ceiling > 0 && limits.CPUMillis > ceiling {
			return nil, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf(
				"sandbox %s declares policy.resources.cpus %s, above the deployment ceiling SANDBOX_MAX_CPUS=%s",
				session.Summary.ID, formatCPUMillis(limits.CPUMillis), formatCPUMillis(ceiling)), nil)
		}
		if ceiling := d.config.SandboxMaxMemoryMiB; ceiling > 0 && limits.MemoryMiB > ceiling {
			return nil, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf(
				"sandbox %s declares policy.resources.memory %dMi, above the deployment ceiling SANDBOX_MAX_MEMORY_MIB=%d",
				session.Summary.ID, limits.MemoryMiB, ceiling), nil)
		}
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

func formatCPUMillis(millis int64) string {
	if millis%1000 == 0 {
		return fmt.Sprintf("%d", millis/1000)
	}
	return fmt.Sprintf("%.3g", float64(millis)/1000)
}
