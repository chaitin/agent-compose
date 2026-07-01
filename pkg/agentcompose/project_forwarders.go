package agentcompose

import (
	"context"

	"connectrpc.com/connect"

	projectspkg "agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

var errRunAgentStreamSend = projectspkg.ErrRunAgentStreamSend

type projectRunStreamSink struct {
	send func(*agentcomposev2.RunAgentStreamResponse) error
}

func (s *Service) projectService() *ProjectService {
	s.projectHandlers = NewProjectServiceFromDeps(s)
	if s.loaders != nil {
		s.loaders.SetProjectAgentRunner(s.projectHandlers)
	}
	return s.projectHandlers
}

func (s *Service) ValidateProject(ctx context.Context, req *connect.Request[agentcomposev2.ValidateProjectRequest]) (*connect.Response[agentcomposev2.ValidateProjectResponse], error) {
	return s.projectService().ValidateProject(ctx, req)
}

func (s *Service) ApplyProject(ctx context.Context, req *connect.Request[agentcomposev2.ApplyProjectRequest]) (*connect.Response[agentcomposev2.ApplyProjectResponse], error) {
	return s.projectService().ApplyProject(ctx, req)
}

func (s *Service) GetProject(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
	return s.projectService().GetProject(ctx, req)
}

func (s *Service) ListProjects(ctx context.Context, req *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
	return s.projectService().ListProjects(ctx, req)
}

func (s *Service) RemoveProject(ctx context.Context, req *connect.Request[agentcomposev2.RemoveProjectRequest]) (*connect.Response[agentcomposev2.RemoveProjectResponse], error) {
	return s.projectService().RemoveProject(ctx, req)
}

func (s *Service) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (ProjectRecord, error) {
	return s.projectService().ResolveProjectRef(ctx, ref)
}

func (s *Service) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	return s.projectService().RunAgent(ctx, req)
}

func (s *Service) runProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest, stream any) (ProjectRunRecord, error, error) {
	var sink *projectspkg.ProjectRunStreamSink
	if stream != nil {
		if projectStream, ok := stream.(*projectRunStreamSink); ok {
			sink = &projectspkg.ProjectRunStreamSink{Send: projectStream.send}
		}
	}
	return s.projectService().RunProjectAgentWithStream(ctx, msg, sink)
}

func (s *Service) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	return s.projectService().RunAgentStream(ctx, req, stream)
}

func (s *Service) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return s.projectService().GetRun(ctx, req)
}

func (s *Service) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	return s.projectService().ListRuns(ctx, req)
}

func (s *Service) StopRun(ctx context.Context, req *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	return s.projectService().StopRun(ctx, req)
}
