package egress

// Headers the sandbox-init process adds to a mediated CONNECT request. The
// daemon treats them as attribution data: the sandbox header must agree with
// the proxy credential, while the session, marker, and pid are recorded for
// audit and never change the policy outcome.
const (
	IdentityHeaderSandbox = "X-Agent-Compose-Sandbox"
	IdentityHeaderSession = "X-Agent-Compose-Session"
	IdentityHeaderMarker  = "X-Agent-Compose-Marker"
	IdentityHeaderPID     = "X-Agent-Compose-Pid"
	// IdentityHeaderBinary carries the absolute executable path of the process
	// that produced the mediated connection, base64-encoded so a path can never
	// break the CONNECT request or inject another header. The sandbox init
	// process is the producer; the proxy embeds it in the request's Binary field
	// after strict validation.
	IdentityHeaderBinary = "X-Agent-Compose-Binary-B64"
)
