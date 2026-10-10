package egress

import "testing"

// The egress decision model is the single place where a declared network policy
// becomes a rule set, so it must carry coverage in the broader shapes and not
// only in the unit shape. These aggregators re-run the package's behavior tests
// under the shape markers the coverage gate keys on, as the other packages do.
func TestIntegrationEgressDecisionWorkflows(t *testing.T) {
	t.Run("decide table", TestDecideTableDriven)
	t.Run("decide first match", TestDecideFirstMatchWins)
	t.Run("decide empty rule", TestDecideEmptyRuleMatchesAnyName)
	t.Run("decide ignores consumer and kind", TestDecideDoesNotUseConsumerOrKind)
	t.Run("host pattern", TestHostPatternMatches)
	t.Run("validate host pattern", TestValidateHostPattern)
	t.Run("endpoint name round trip", TestEndpointNameRoundTrip)
	t.Run("endpoint pattern does not widen", TestFormatEndpointPatternDoesNotWidenAnInvalidProtocol)
	t.Run("protocol inspectable", TestProtocolInspectable)
	t.Run("engine endpoints from URLs", TestEngineEndpointsFromURLs)
	t.Run("compile deny", TestCompileNetworkPolicyDenyAllowsDeclaredAndEngineEndpoints)
	t.Run("compile rejects invalid declaration", TestCompileNetworkPolicyRejectsInvalidDeclaration)
	t.Run("undeclared is unrestricted", TestEffectiveNetworkPolicyUndeclaredIsUnrestricted)
	t.Run("engine rules precede declared rules", TestCompileNetworkPolicyEngineRulesPrecedeDeclaredRules)
	t.Run("rule match mode", TestRuleMatchModeChangesGeneration)
	t.Run("catch-all endpoint rule", TestEndpointRuleWithoutNamesMatchesAnyEndpoint)
	t.Run("malformed endpoint request", TestEndpointRuleDoesNotMatchMalformedRequest)
	t.Run("generation is content derived", TestNewPolicyGenerationIsContentDerived)
	t.Run("policy copies caller rules", TestNewPolicyCopiesCallerRules)
	t.Run("rules returns a copy", TestRulesReturnsCopy)
	t.Run("generation override", TestWithGenerationOverridesContentGeneration)
	t.Run("decision staleness", TestDecisionStaleness)
	t.Run("network policy report", TestReportNetworkPolicy)
	t.Run("report matches compiled engine rules", TestReportNetworkPolicyMatchesCompiledEngineRules)
}

func TestE2EEgressDecisionWorkflows(t *testing.T) {
	TestIntegrationEgressDecisionWorkflows(t)
}
