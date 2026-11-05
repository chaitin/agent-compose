package sandboxinit

import (
	"encoding/json"
	"time"
)

// Version is the control protocol version. It is sent by the client on every
// request and checked by the server, so a daemon and a guest init from
// different builds fail with a clear message instead of misreading frames.
const Version = 1

// Capability identifiers the init server advertises in its handshake. A daemon
// and a guest image are built and shipped independently, so additive features
// are negotiated instead of inferred from the protocol version: an old init
// keeps answering the requests it knows and a new client treats a missing
// capability as "not available" rather than as a failure.
const (
	// CapabilityExec is the exec request and its stdout/stderr/exit frames.
	CapabilityExec = "exec"
	// CapabilityInteraction is stdin, resize, and signal on one exec session.
	CapabilityInteraction = "interaction"
	// CapabilityKillTree is marker-based process-tree termination.
	CapabilityKillTree = "kill-tree"
	// CapabilityProcessList is the diagnostic process listing.
	CapabilityProcessList = "process-list"
	// CapabilitySandboxIdentity is the pong handshake that names the sandbox
	// this init belongs to.
	CapabilitySandboxIdentity = "sandbox-identity"
	// CapabilityEgressAudit is seccomp user-notification observation of
	// workload connect calls. It is advertised only when the guest kernel and
	// the configured mode actually provide it.
	CapabilityEgressAudit = "egress-audit"
	// CapabilityEgressMediate is seccomp user-notification mediation: every
	// workload connect is redirected through the daemon egress proxy.
	CapabilityEgressMediate = "egress-mediate"
	// CapabilityEgressDNS is the policy DNS stub that answers A queries with
	// synthetic addresses and passes engine-owned names through.
	CapabilityEgressDNS = "egress-dns"
	// CapabilityEgressHardening is the listener-less deny filter installed
	// after the notification listener is handed over.
	CapabilityEgressHardening = "egress-hardening"
	// CapabilityLandlockEnforce is filesystem confinement of every workload:
	// init resolved a Landlock policy and its exec trampoline applies it. It is
	// advertised only when the guest kernel supports Landlock and init did not
	// degrade under the configured fallback.
	CapabilityLandlockEnforce = "landlock-enforce"
)

// SandboxIDEnvVar is the environment variable that carries the sandbox ID into
// the init process. It travels through the driver environment instead of a new
// command-line flag so a daemon upgrade never makes an older guest init reject
// its own entrypoint arguments.
const SandboxIDEnvVar = "SANDBOX_ID"

// ServerCapabilities returns the capabilities every init build of this protocol
// version offers. It returns a fresh slice so a caller cannot mutate a shared
// capability list.
func ServerCapabilities() []string {
	return []string{
		CapabilityExec,
		CapabilityInteraction,
		CapabilityKillTree,
		CapabilityProcessList,
		CapabilitySandboxIdentity,
	}
}

// Request types understood by the init control socket.
const (
	RequestExec     = "exec"
	RequestKill     = "kill"
	RequestKillTree = "kill_tree"
	RequestList     = "list"
	RequestPing     = "ping"
	RequestShutdown = "shutdown"
	// RequestConfigureEgress delivers the supervisor relay token over the
	// authenticated init control socket. It is used instead of the container
	// environment so a Docker exec process cannot read the token.
	RequestConfigureEgress = "configure_egress"
	// Control frames sent by the client inside an exec session.
	RequestStdin    = "stdin"
	RequestStdinEOF = "stdin_eof"
	RequestResize   = "resize"
	RequestSignal   = "signal"
)

// Frame types emitted by the init server.
const (
	FrameStarted    = "started"
	FrameStdout     = "stdout"
	FrameStderr     = "stderr"
	FrameExit       = "exit"
	FrameError      = "error"
	FrameKilled     = "killed"
	FrameProcesses  = "processes"
	FramePong       = "pong"
	FrameShutdown   = "shutdown"
	FrameConfigured = "configured"
)

// Request is one control message. Exactly one operation is selected by Type.
// A request and the frames it produces share one Unix-socket connection.
type Request struct {
	Version   int               `json:"version"`
	Type      string            `json:"type"`
	SessionID string            `json:"session_id,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	TTY       bool              `json:"tty,omitempty"`
	Rows      uint16            `json:"rows,omitempty"`
	Cols      uint16            `json:"cols,omitempty"`
	// Marker tags a process tree so the daemon can terminate it without
	// holding the exec connection. It is also inherited as an environment
	// variable by descendants.
	Marker string `json:"marker,omitempty"`
	// Tree asks Kill to terminate the whole process group, not only the
	// session leader.
	Tree bool `json:"tree,omitempty"`
	// Signal is a portable signal name (TERM, KILL, INT, HUP).
	Signal string `json:"signal,omitempty"`
	// Data is stdin bytes. JSON encodes []byte as base64.
	Data []byte `json:"data,omitempty"`
	// Token is the per-sandbox supervisor relay token. It is sent only over
	// the authenticated init control socket and never stored in the container
	// environment.
	Token string `json:"token,omitempty"`
}

// Frame is one server message. Data is raw stdout/stderr bytes; JSON encodes
// it as base64 so the protocol remains text-only and easy to inspect.
type Frame struct {
	Type      string        `json:"type"`
	SessionID string        `json:"session_id,omitempty"`
	Data      []byte        `json:"data,omitempty"`
	Code      int           `json:"code,omitempty"`
	Message   string        `json:"message,omitempty"`
	Processes []ProcessInfo `json:"processes,omitempty"`
	// SandboxID and Capabilities are only carried by the pong handshake. They
	// are empty when the other side is an init build that predates capability
	// negotiation, which is why callers must treat empty as "legacy".
	SandboxID    string   `json:"sandbox_id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// MarshalJSON keeps non-exit frames compact while making the exit status
// explicit: an exit frame always carries code, including a successful zero.
// The plain omitempty tag would otherwise make exit 0 indistinguishable from a
// missing field for clients outside the Go SDK.
func (f Frame) MarshalJSON() ([]byte, error) {
	type wireFrame struct {
		Type         string        `json:"type"`
		SessionID    string        `json:"session_id,omitempty"`
		Data         []byte        `json:"data,omitempty"`
		Code         *int          `json:"code,omitempty"`
		Message      string        `json:"message,omitempty"`
		Processes    []ProcessInfo `json:"processes,omitempty"`
		SandboxID    string        `json:"sandbox_id,omitempty"`
		Capabilities []string      `json:"capabilities,omitempty"`
	}
	out := wireFrame{
		Type:         f.Type,
		SessionID:    f.SessionID,
		Data:         f.Data,
		Message:      f.Message,
		Processes:    f.Processes,
		SandboxID:    f.SandboxID,
		Capabilities: f.Capabilities,
	}
	if f.Type == FrameExit {
		code := f.Code
		out.Code = &code
	}
	return json.Marshal(out)
}

// ProcessInfo describes one process currently owned by init. It is diagnostic:
// the daemon does not use it to make policy decisions.
type ProcessInfo struct {
	ID        string    `json:"id"`
	PID       int       `json:"pid"`
	Command   string    `json:"command"`
	Args      []string  `json:"args,omitempty"`
	Marker    string    `json:"marker,omitempty"`
	TTY       bool      `json:"tty,omitempty"`
	StartedAt time.Time `json:"started_at"`
	Main      bool      `json:"main,omitempty"`
}
