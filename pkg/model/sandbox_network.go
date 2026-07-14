package model

type SandboxNetworkIntent struct {
	ProjectID   string                     `json:"project_id"`
	ProjectName string                     `json:"project_name"`
	AgentName   string                     `json:"agent_name"`
	Attachments []SandboxNetworkAttachment `json:"attachments,omitempty"`
	Expose      []SandboxNetworkPort       `json:"expose,omitempty"`
	Ports       []SandboxPublishedPort     `json:"ports,omitempty"`
}

type SandboxNetworkAttachment struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}

type SandboxNetworkPort struct {
	Target   int    `json:"target"`
	Protocol string `json:"protocol"`
}

type SandboxPublishedPort struct {
	HostIP    string `json:"host_ip"`
	Published int    `json:"published,omitempty"`
	Target    int    `json:"target"`
	Protocol  string `json:"protocol"`
}

type SandboxNetworkState struct {
	Deployment       string                   `json:"deployment"`
	ServiceCIDR      string                   `json:"service_cidr,omitempty"`
	Isolation        string                   `json:"isolation,omitempty"`
	Attachments      []SandboxNetworkEndpoint `json:"attachments,omitempty"`
	Bindings         []SandboxPortBinding     `json:"bindings,omitempty"`
	AllowedAddresses []string                 `json:"allowed_addresses,omitempty"`
}

type SandboxNetworkEndpoint struct {
	Name               string `json:"name"`
	RuntimeNetworkName string `json:"runtime_network_name"`
	HostGateway        string `json:"host_gateway"`
	DaemonAddress      string `json:"daemon_address,omitempty"`
}

type SandboxPortBinding struct {
	Network    string `json:"network,omitempty"`
	HostIP     string `json:"host_ip"`
	HostPort   int    `json:"host_port"`
	GuestPort  int    `json:"guest_port"`
	Protocol   string `json:"protocol"`
	Visibility string `json:"visibility"`
	Publisher  string `json:"publisher"`
}

func CloneSandboxNetworkIntent(intent *SandboxNetworkIntent) *SandboxNetworkIntent {
	if intent == nil {
		return nil
	}
	clone := *intent
	clone.Attachments = append([]SandboxNetworkAttachment(nil), intent.Attachments...)
	clone.Expose = append([]SandboxNetworkPort(nil), intent.Expose...)
	clone.Ports = append([]SandboxPublishedPort(nil), intent.Ports...)
	return &clone
}

func CloneSandboxNetworkState(state *SandboxNetworkState) *SandboxNetworkState {
	if state == nil {
		return nil
	}
	clone := *state
	clone.Attachments = append([]SandboxNetworkEndpoint(nil), state.Attachments...)
	clone.Bindings = append([]SandboxPortBinding(nil), state.Bindings...)
	clone.AllowedAddresses = append([]string(nil), state.AllowedAddresses...)
	return &clone
}
