// The wire contract, kept in its own module so a client can depend on it
// without inheriting the daemon's runtime dependencies. The API version lives
// in the package path (agentcompose/v2), not in the module path: a breaking
// change ships as a new package beside the old one, and only removing an old
// package is a module major version bump.
module github.com/chaitin/agent-compose/proto

go 1.24.0

require (
	connectrpc.com/connect v1.19.2
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af
)
