package egress

import "testing"

// TestIntegrationEgressPolicyWorkflows and TestE2EEgressPolicyWorkflows run the
// package's policy, decision, endpoint, and record tests inside the integration
// and E2E coverage shapes. pkg/egress owns the single outbound decision model
// that every driver, the LLM facade, and the capability proxy compile into, so
// the same behavior belongs to every shape rather than only to unit coverage.
func TestIntegrationEgressPolicyWorkflows(t *testing.T) {
	t.Run("validate host pattern", TestValidateHostPattern)
	t.Run("match host pattern", TestHostPatternMatches)
	t.Run("endpoint name round trip", TestEndpointNameRoundTrip)
	t.Run("protocol inspectability", TestProtocolInspectable)
	t.Run("engine endpoints from urls", TestEngineEndpointsFromURLs)
	t.Run("undeclared policy is unrestricted", TestEffectiveNetworkPolicyUndeclaredIsUnrestricted)
	t.Run("compile deny allows declared and engine endpoints", TestCompileNetworkPolicyDenyAllowsDeclaredAndEngineEndpoints)
	t.Run("compile engine rules precede declared rules", TestCompileNetworkPolicyEngineRulesPrecedeDeclaredRules)
	t.Run("compile rejects invalid declaration", TestCompileNetworkPolicyRejectsInvalidDeclaration)
	t.Run("new policy copies caller rules", TestNewPolicyCopiesCallerRules)
	t.Run("generation is content derived", TestNewPolicyGenerationIsContentDerived)
	t.Run("match mode changes generation", TestRuleMatchModeChangesGeneration)
	t.Run("with generation overrides content generation", TestWithGenerationOverridesContentGeneration)
	t.Run("rules returns a copy", TestRulesReturnsCopy)
	t.Run("decide table driven", TestDecideTableDriven)
	t.Run("decide first match wins", TestDecideFirstMatchWins)
	t.Run("decide empty rule matches any name", TestDecideEmptyRuleMatchesAnyName)
	t.Run("decide ignores consumer and kind", TestDecideDoesNotUseConsumerOrKind)
	t.Run("endpoint rule rejects malformed request", TestEndpointRuleDoesNotMatchMalformedRequest)
	t.Run("decision staleness", TestDecisionStaleness)
}

func TestE2EEgressPolicyWorkflows(t *testing.T) {
	TestIntegrationEgressPolicyWorkflows(t)
}
