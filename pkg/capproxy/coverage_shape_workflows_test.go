package capproxy

import "testing"

func TestIntegrationCapabilityProxyWorkflows(t *testing.T) {
	t.Run("metadata", TestProxyInjectsOctoBusMetadata)
	t.Run("guest instance", TestProxyForwardsGuestInstance)
	t.Run("missing instance", TestProxyRejectsMissingInstanceForBusinessCall)
	t.Run("capset denied", TestProxyRejectsCapsetOutsideAllowedSet)
	t.Run("missing token", TestProxyRejectsMissingSandboxToken)
	t.Run("capset egress policy mirrors binding", TestCapsetEgressPolicyMirrorsBinding)
	t.Run("resolve call capset contract", TestResolveCallCapsetContract)
}

func TestE2ECapabilityProxyWorkflows(t *testing.T) {
	TestIntegrationCapabilityProxyWorkflows(t)
}
