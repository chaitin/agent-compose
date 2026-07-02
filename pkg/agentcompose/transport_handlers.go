package agentcompose

import (
	"agent-compose/internal/agentcompose/transport/connectv1"
	"agent-compose/internal/agentcompose/transport/connectv2"
)

type SessionHandler = connectv1.SessionHandler
type KernelHandler = connectv1.KernelHandler
type AgentHandler = connectv1.AgentHandler
type AgentDefinitionHandler = connectv1.AgentDefinitionHandler
type LLMHandler = connectv1.LLMHandler
type ConfigHandler = connectv1.ConfigHandler
type CapabilityHandler = connectv1.CapabilityHandler
type DashboardHandler = connectv1.DashboardHandler
type LoaderHandler = connectv1.LoaderHandler

type ProjectHandler = connectv2.ProjectHandler
type ImageHandler = connectv2.ImageHandler
type RunHandler = connectv2.RunHandler
type ExecHandler = connectv2.ExecHandler

func NewSessionHandler(service *Service) *SessionHandler {
	return connectv1.NewSessionHandler(service)
}

func NewKernelHandler(service *Service) *KernelHandler {
	return connectv1.NewKernelHandler(service)
}

func NewAgentHandler(service *Service) *AgentHandler {
	return connectv1.NewAgentHandler(service)
}

func NewAgentDefinitionHandler(service *Service) *AgentDefinitionHandler {
	return connectv1.NewAgentDefinitionHandler(service)
}

func NewLLMHandler(service *Service) *LLMHandler {
	return connectv1.NewLLMHandler(service)
}

func NewConfigHandler(service *Service) *ConfigHandler {
	return connectv1.NewConfigHandler(service)
}

func NewCapabilityHandler(service *Service) *CapabilityHandler {
	return connectv1.NewCapabilityHandler(service)
}

func NewDashboardHandler(service *Service) *DashboardHandler {
	return connectv1.NewDashboardHandler(service)
}

func NewLoaderHandler(service *Service) *LoaderHandler {
	return connectv1.NewLoaderHandler(service)
}

func NewProjectHandler(service *Service) *ProjectHandler {
	return connectv2.NewProjectHandler(service)
}

func NewImageHandler(service *Service) *ImageHandler {
	return connectv2.NewImageHandler(service)
}

func NewRunHandler(service *Service) *RunHandler {
	return connectv2.NewRunHandler(service)
}

func NewExecHandler(service *Service) *ExecHandler {
	return connectv2.NewExecHandler(service)
}
