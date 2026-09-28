package runs

import (
	"context"
	"fmt"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// UnfinishedSandboxRunStore finds project runs that still hold a sandbox.
type UnfinishedSandboxRunStore interface {
	UnfinishedProjectRunForSandbox(ctx context.Context, sandboxID, excludeRunID string) (string, bool, error)
}

// rejectCrossIdentitySandboxReuse refuses to reuse a sandbox that another
// unfinished run is using under a different identity. A sandbox has a single
// capability token, so rebinding it to this run's trusted headers would make
// capability calls from the other run carry this run's identity, and the
// reverse. Reuse under the same identity, or once the other run has finished,
// is unaffected.
func (c *Controller) rejectCrossIdentitySandboxReuse(ctx context.Context, sandboxID, runID string, trustedHeaders []domain.TrustedHeader) error {
	if c.capTokens == nil || !c.capTokens.TrustedHeadersConflict(sandboxID, trustedHeaders) {
		return nil
	}
	otherRunID, found, err := c.configDB.UnfinishedProjectRunForSandbox(ctx, sandboxID, runID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("sandbox %s is in use by run %s under a different identity", sandboxID, otherRunID), nil)
}
