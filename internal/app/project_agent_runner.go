package app

import (
	"context"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type serviceProjectAgentRunner struct {
	service *Service
}

func (r serviceProjectAgentRunner) RunProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error) {
	if r.service == nil {
		return ProjectRunRecord{}, nil, nil
	}
	return r.service.runProjectAgent(ctx, msg, nil)
}
