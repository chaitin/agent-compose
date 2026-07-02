package agentcompose

import (
	"context"

	"connectrpc.com/connect"

	executorpkg "agent-compose/pkg/executor"
	"agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	return s.execService().Exec(ctx, req)
}

func (s *Service) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	return s.execService().ExecStream(ctx, req, stream)
}

func (s *Service) execService() *executorpkg.Service {
	return executorpkg.NewService(s.config, s.store, s.runtimes, projects.NewExecTargetResolver(s.configDB, s.store))
}
