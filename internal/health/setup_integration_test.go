package health

import "testing"

func TestIntegrationHealthServiceWorkflow(t *testing.T) {
	testHealthServiceWorkflow(t)
}

func TestE2EHealthServiceWorkflow(t *testing.T) {
	testHealthServiceWorkflow(t)
}

func testHealthServiceWorkflow(t *testing.T) {
	t.Helper()
	t.Run("snapshot includes runtime details", testServiceSnapshotIncludesRuntimeDetails)
	t.Run("process usage updates probe", testProcessUsageUpdatesProbe)
	t.Run("rpc status and setup", testRPCStatusAndSetup)
	t.Run("rpc watch status sends initial snapshot", testRPCWatchStatusSendsInitialSnapshot)
	t.Run("process stats helpers", testProcessStatsHelpers)
}
