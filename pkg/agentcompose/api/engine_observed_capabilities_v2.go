package api

import (
	"github.com/chaitin/agent-compose/pkg/capmatrix"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// engineObservedCapabilitiesV2 maps the engine-measured layer to the contract.
// These entries carry source = measured or simulated; unlike the per-driver
// matrix they were never produced by asking a driver whether it is ready.
func engineObservedCapabilitiesV2(observations []capmatrix.ObservedCapability) []*agentcomposev2.EngineObservedCapability {
	out := make([]*agentcomposev2.EngineObservedCapability, 0, len(observations))
	for _, capability := range observations {
		out = append(out, &agentcomposev2.EngineObservedCapability{
			Dimension:     string(capability.Dimension),
			State:         string(capability.State),
			Enforced:      capability.Enforced,
			Mechanism:     capability.Mechanism,
			Preconditions: append([]string(nil), capability.Preconditions...),
			Observed:      capability.Observed,
			Missing:       capability.Missing,
			Source:        string(capability.Source),
		})
	}
	return out
}
