package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// errNetworkPolicyRejected marks a project candidate the daemon-wide network
// policy refuses. The projects controller runs the admission before it writes a
// project or revision, so the rejection leaves no state behind. projectConnectError
// maps it to InvalidArgument because the caller can fix the declaration.
var errNetworkPolicyRejected = errors.New("network policy rejected")

// networkPolicyAdmission returns the pre-write admission check for a project
// candidate. It compiles the effective network policy of every agent exactly as
// the apply report does, so a declaration that cannot layer under the
// daemon-wide policy is refused before the project is persisted.
func networkPolicyAdmission(mediation sandboxEgressMediation, config *appconfig.Config) func(context.Context, *compose.NormalizedProjectSpec) error {
	return func(_ context.Context, spec *compose.NormalizedProjectSpec) error {
		if _, err := agentNetworkPolicyReports(spec, mediation, config); err != nil {
			return fmt.Errorf("%w: %w", errNetworkPolicyRejected, err)
		}
		return nil
	}
}
