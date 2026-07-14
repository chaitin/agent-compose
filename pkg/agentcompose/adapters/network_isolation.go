package adapters

import (
	"context"
	"fmt"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/networks"
)

type RuntimeIsolationPolicy struct {
	Enforce bool
}

func (p RuntimeIsolationPolicy) Validate(_ context.Context, sandbox *domain.Sandbox) error {
	if !p.Enforce || sandbox == nil || sandbox.NetworkIntent == nil || len(sandbox.NetworkIntent.Attachments) == 0 {
		return nil
	}
	switch sandbox.Summary.Driver {
	case driverpkg.RuntimeDriverMicrosandbox:
		return nil
	case driverpkg.RuntimeDriverDocker:
		return fmt.Errorf("%w: strict Docker network isolation requires a physical-host controller", networks.ErrUnsupported)
	case driverpkg.RuntimeDriverBoxlite:
		return fmt.Errorf("%w: strict BoxLite network isolation is unavailable because host alias traffic bypasses its allowlist", networks.ErrUnsupported)
	default:
		return fmt.Errorf("%w: strict isolation is unavailable for runtime %q", networks.ErrUnsupported, sandbox.Summary.Driver)
	}
}

func (p RuntimeIsolationPolicy) Evaluate(_ context.Context, sandbox *domain.Sandbox, state *domain.SandboxNetworkState) (string, error) {
	if sandbox == nil || state == nil || len(state.Attachments) == 0 {
		return networks.IsolationNotApplicable, nil
	}
	switch sandbox.Summary.Driver {
	case driverpkg.RuntimeDriverMicrosandbox:
		return networks.IsolationEnforced, nil
	case driverpkg.RuntimeDriverDocker:
	case driverpkg.RuntimeDriverBoxlite:
	default:
		return "", fmt.Errorf("%w: strict isolation is unavailable for runtime %q", networks.ErrUnsupported, sandbox.Summary.Driver)
	}
	return networks.IsolationUnprotected, nil
}
