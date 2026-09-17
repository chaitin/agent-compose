package llms

import (
	"errors"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// OptionalFacadeConfigError reports whether err means only that the daemon has
// no managed LLM configuration for this agent, so the agent may fall back to
// credentials it carries itself.
//
// An ambiguous default connection is never optional. The daemon is configured,
// just not unambiguously, and an agent that silently falls back reports the real
// cause as a credential or connectivity failure instead of the configuration
// error the operator has to fix.
func OptionalFacadeConfigError(err error) bool {
	if err == nil || errors.Is(err, ErrAmbiguousDefaultConnection) {
		return false
	}
	return errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrFailedPrecondition)
}
