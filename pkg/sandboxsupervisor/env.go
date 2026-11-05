package sandboxsupervisor

// Environment variables the daemon uses to hand the supervisor its secret and
// endpoint configuration. They are read once by cmd/ac-supervisor and removed
// from its environment before the server starts.
const (
	// EnvToken carries the per-sandbox random token.
	EnvToken = "SANDBOX_SUPERVISOR_TOKEN"
	// EnvProxy carries the daemon forward proxy URL with the sandbox
	// credential embedded.
	EnvProxy = "SANDBOX_SUPERVISOR_PROXY"
	// EnvDirectEndpoints carries comma-separated engine-owned destinations.
	EnvDirectEndpoints = "SANDBOX_SUPERVISOR_DIRECT_ENDPOINTS"
)
