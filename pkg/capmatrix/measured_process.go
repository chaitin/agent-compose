package capmatrix

import (
	"fmt"
	"strconv"

	"github.com/chaitin/agent-compose/pkg/driver"
)

// MeasuredProcessIsolationCapabilities converts the isolation failures a
// lower layer reported on exec stderr into measured capability assertions.
//
// This is the SEC-3 reconnection of the silenced failure signals: a report of
// "seccomp not available, unable to set seccomp privileges!" becomes
// enforced=false with the closed reason lower_layer_unavailable, instead of
// disappearing into a filter. The facts are engine-measured: they come from
// observing the lower layer's own output, never from asking a driver whether
// it is ready.
func MeasuredProcessIsolationCapabilities(driverName string, facts driver.ExecSecurityFacts) []ObservedCapability {
	out := make([]ObservedCapability, 0, 2)
	if facts.SeccompUnavailable > 0 {
		out = append(out, ObservedCapability{
			Dimension: ObservedProcessSeccomp,
			State:     StateUnsupported,
			Mechanism: ReasonLowerLayerUnavailable,
			Observed:  lowerLayerObservation(driverName, "seccomp privileges were not set", facts.SeccompUnavailable),
			Missing:   "the sandbox's lower layer reported it could not set seccomp privileges",
			Source:    SourceMeasured,
		})
	}
	if facts.NoNewPrivilegesUnavailable > 0 {
		out = append(out, ObservedCapability{
			Dimension: ObservedProcessNoNewPrivileges,
			State:     StateUnsupported,
			Mechanism: ReasonLowerLayerUnavailable,
			Observed:  lowerLayerObservation(driverName, "no_new_privileges was not enforced", facts.NoNewPrivilegesUnavailable),
			Missing:   "the sandbox's lower layer reported it could not enforce no_new_privileges",
			Source:    SourceMeasured,
		})
	}
	return out
}

func lowerLayerObservation(driverName, message string, count int) string {
	prefix := "the sandbox lower layer"
	if driverName != "" {
		prefix = fmt.Sprintf("driver %q's sandbox lower layer", driverName)
	}
	return prefix + " reported on exec stderr that " + message + " (" + strconv.Itoa(count) + " time(s))"
}
