package agentcompose

import (
	"context"

	loaderspkg "agent-compose/pkg/loaders"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectAgentRunner = loaderspkg.ProjectAgentRunner

type serviceProjectAgentRunner struct {
	service *Service
}

func (r serviceProjectAgentRunner) RunProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error) {
	return r.service.projectService().RunProjectAgent(ctx, msg)
}
