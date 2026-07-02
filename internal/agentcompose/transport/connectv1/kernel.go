package connectv1

import "agent-compose/proto/agentcompose/v1/agentcomposev1connect"

type KernelHandler struct {
	agentcomposev1connect.KernelServiceHandler
}

func NewKernelHandler(service agentcomposev1connect.KernelServiceHandler) *KernelHandler {
	return &KernelHandler{KernelServiceHandler: service}
}
