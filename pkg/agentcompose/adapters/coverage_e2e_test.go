package adapters

import "testing"

func TestAdapterIntegrationWorkflowE2E(t *testing.T) {
	t.Run("runtime", TestIntegrationAdapterRuntimeWorkflows)
	t.Run("network", TestIntegrationAdapterSandboxNetworkWorkflows)
}
