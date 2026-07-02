package agents

import "testing"

func TestIntegrationAgentDefinitionWorkflow(t *testing.T) {
	TestAgentDefinitionValidationAndProtoMapping(t)
	TestAgentDefinitionCreateSession(t)
	TestAgentDefinitionCreateSessionUsesDefinitionCapsets(t)
	TestDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t)
}

func TestE2EAgentDefinitionWorkflow(t *testing.T) {
	TestAgentDefinitionValidationAndProtoMapping(t)
	TestAgentDefinitionCreateSession(t)
	TestAgentDefinitionCreateSessionUsesDefinitionCapsets(t)
	TestDeleteAgentDefinitionStopsSessionsAndKeepsDeletedInList(t)
}
