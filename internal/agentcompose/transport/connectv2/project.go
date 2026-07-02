package connectv2

import "agent-compose/proto/agentcompose/v2/agentcomposev2connect"

type ProjectHandler struct {
	agentcomposev2connect.ProjectServiceHandler
}

func NewProjectHandler(service agentcomposev2connect.ProjectServiceHandler) *ProjectHandler {
	return &ProjectHandler{ProjectServiceHandler: service}
}
