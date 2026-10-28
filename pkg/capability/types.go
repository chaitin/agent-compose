package capability

import "time"

const (
	ProtocolGRPC    = "grpc"
	ProtocolMCP     = "mcp"
	ProtocolConnect = "connect"
)

const (
	// ProjectScopeHeader carries the sandbox's agent-compose project scope to
	// the daemon-wide capability gateway, so an upstream gateway can attribute
	// a directory read that arrives without a caller identity. It mirrors the
	// header capproxy forwards on the gateway data path.
	ProjectScopeHeader = "x-octobus-project-id"
	// ExtUsernameHeader carries the user a directory read runs on behalf of.
	// Its trusted-ingress form is x-mpi-username.
	ExtUsernameHeader = "x-octobus-ext-username"
)

// Scope is the optional project and user context a directory read attaches to
// its request. The zero value sends no context headers, which keeps legacy
// gateways and direct OctoBus deployments on their existing behavior.
type Scope struct {
	ProjectID string
	Username  string
}

type Config struct {
	Addr    string
	Token   string
	Timeout time.Duration
	Scope   Scope
}

type Status struct {
	Configured   bool
	OK           bool
	Status       string
	ServiceCount uint32
	Error        string
}

type Capset struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
}

type Endpoint struct {
	Protocol     string
	Endpoint     string
	MethodPath   string
	Metadata     map[string]string
	ToolName     string
	Procedure    string
	HTTPMethod   string
	ContentTypes []string
}

type Method struct {
	ServiceID               string
	InstanceID              string
	RuntimeMode             string
	MethodFullName          string
	RequestMessageFullName  string
	ResponseMessageFullName string
	BackendInstanceStatus   string
	Endpoints               []Endpoint
}

type Catalog struct {
	CapsetID    string
	Name        string
	Description string
	Methods     []Method
}
