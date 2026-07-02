package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type LoaderHandler struct {
	agentcomposev1connect.LoaderServiceHandler
}

func NewLoaderHandler(service agentcomposev1connect.LoaderServiceHandler) *LoaderHandler {
	return &LoaderHandler{LoaderServiceHandler: service}
}
