package adapters

import (
	"log/slog"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
)

// reportMeasuredLowerLayerIsolationFacts turns the isolation failures a driver
// observed on one exec's stderr into measured capability assertions and logs
// them. This is the production consumer of the reconnected lower-layer signals:
// the exec has already run, so the engine cannot retroactively enforce the
// dimension, and the honest outcome is a high-severity report next to the
// preserved stderr.
//
// It lives on the driver-facing adapter boundary because that is the only layer
// that sees every exec result and may import pkg/capmatrix: pkg/driver cannot,
// since pkg/capmatrix imports it.
func reportMeasuredLowerLayerIsolationFacts(sandboxID, driverName string, facts driverpkg.ExecSecurityFacts) {
	for _, capability := range capmatrix.MeasuredProcessIsolationCapabilities(driverName, facts) {
		slog.Warn("sandbox lower layer reported an isolation failure",
			"sandbox_id", sandboxID,
			"driver", driverName,
			"dimension", string(capability.Dimension),
			"state", string(capability.State),
			"mechanism", capability.Mechanism,
			"missing", capability.Missing,
			"observed", capability.Observed,
		)
	}
}
