package connectv2

import "agent-compose/proto/agentcompose/v2/agentcomposev2connect"

type ImageHandler struct {
	agentcomposev2connect.ImageServiceHandler
}

func NewImageHandler(service agentcomposev2connect.ImageServiceHandler) *ImageHandler {
	return &ImageHandler{ImageServiceHandler: service}
}
