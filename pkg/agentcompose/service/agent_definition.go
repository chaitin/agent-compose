package agentcompose

import agentcomposev1 "agent-compose/proto/agentcompose/v1"

type AgentValidationResult struct {
	Availability agentcomposev1.AgentAvailabilityStatus
	Health       agentcomposev1.AgentHealthStatus
	Warnings     []string
	Errors       []string
}
