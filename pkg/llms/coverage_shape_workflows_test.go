package llms

import "testing"

// TestIntegrationFacadeEgressWorkflows and TestE2EFacadeEgressWorkflows run the
// facade egress contract in the integration and E2E coverage shapes. The facade
// decision is what a running sandbox actually reaches on the network, so its
// contract belongs to every shape rather than only to unit coverage.
func TestIntegrationFacadeEgressWorkflows(t *testing.T) {
	t.Run("facade egress golden table", TestFacadeEgressPolicyGolden)
	t.Run("allowed facade model carries no deny reason", TestFacadeEgressPolicyAllowCarriesNoDenyReason)
	t.Run("facade egress generation tracks token content", TestFacadeEgressPolicyGenerationTracksTokenContent)
}

func TestE2EFacadeEgressWorkflows(t *testing.T) {
	TestIntegrationFacadeEgressWorkflows(t)
}
