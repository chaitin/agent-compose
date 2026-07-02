package loader

import (
	"context"
	"fmt"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectAgentRunner interface {
	RunProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error)
}

type unavailableProjectAgentRunner struct{}

func (unavailableProjectAgentRunner) RunProjectAgent(context.Context, *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error) {
	return ProjectRunRecord{}, nil, fmt.Errorf("project agent runner is not configured")
}

func (m *LoaderManager) SetProjectAgentRunner(runner ProjectAgentRunner) {
	if m == nil || runner == nil {
		return
	}
	m.projectAgentRunner = runner
}

func (m *LoaderManager) projectAgentRunnerComponent() ProjectAgentRunner {
	m.initLoaderComponents()
	return m.projectAgentRunner
}
