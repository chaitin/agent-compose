package capability

import (
	"encoding/json"
	"time"
)

const (
	ProtocolGRPC    = "grpc"
	ProtocolMCP     = "mcp"
	ProtocolConnect = "connect"
)

type Config struct {
	Addr       string
	Token      string
	AdminToken string
	Timeout    time.Duration
}

type InvokeRequest struct {
	CapsetID   string
	InstanceID string
	ServiceID  string
	Method     string
	Payload    json.RawMessage
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
