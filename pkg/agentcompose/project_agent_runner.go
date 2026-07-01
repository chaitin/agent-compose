package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	loaderspkg "agent-compose/pkg/loaders"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ProjectAgentRunner = loaderspkg.ProjectAgentRunner

type serviceProjectAgentRunner struct {
	service *Service
}

func serviceProjectAgentRunnerFromDI(di do.Injector) ProjectAgentRunner {
	return serviceProjectAgentRunner{service: &Service{
		config:   do.MustInvoke[*appconfig.Config](di),
		store:    do.MustInvoke[*Store](di),
		configDB: do.MustInvoke[*ConfigStore](di),
		driver:   do.MustInvoke[Driver](di),
		executor: do.MustInvoke[*Executor](di),
		images:   NewDockerImageBackend(),
		streams:  do.MustInvoke[*SessionStreamBroker](di),
	}}
}

func (r serviceProjectAgentRunner) RunProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error) {
	return r.service.runProjectAgent(ctx, msg, nil)
}
