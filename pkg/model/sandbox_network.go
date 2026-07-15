package model

import "time"

type SandboxNetworkDefinition struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}

type SandboxNetworkIntent struct {
	Version         int                        `json:"version"`
	ProjectID       string                     `json:"project_id"`
	ProjectRevision int64                      `json:"project_revision"`
	AgentName       string                     `json:"agent_name"`
	Definitions     []SandboxNetworkDefinition `json:"definitions"`
	Attachments     []string                   `json:"attachments"`
	Expose          []string                   `json:"expose,omitempty"`
	Ports           []string                   `json:"ports,omitempty"`
}

func CloneSandboxNetworkIntent(intent *SandboxNetworkIntent) *SandboxNetworkIntent {
	if intent == nil {
		return nil
	}
	cloned := *intent
	cloned.Definitions = append([]SandboxNetworkDefinition(nil), intent.Definitions...)
	cloned.Attachments = append([]string(nil), intent.Attachments...)
	cloned.Expose = append([]string(nil), intent.Expose...)
	cloned.Ports = append([]string(nil), intent.Ports...)
	return &cloned
}

type SandboxNetworkAttachmentState struct {
	LogicalName string   `json:"logical_name"`
	RuntimeName string   `json:"runtime_name"`
	NetworkID   string   `json:"network_id,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	IPv4Address string   `json:"ipv4_address,omitempty"`
	Primary     bool     `json:"primary,omitempty"`
}

type SandboxPortBindingState struct {
	ContainerPort string `json:"container_port"`
	HostIP        string `json:"host_ip"`
	HostPort      string `json:"host_port"`
}

type SandboxNetworkState struct {
	Mode         string                          `json:"mode"`
	Attachments  []SandboxNetworkAttachmentState `json:"attachments,omitempty"`
	PortBindings []SandboxPortBindingState       `json:"port_bindings,omitempty"`
	ReconciledAt time.Time                       `json:"reconciled_at"`
}
