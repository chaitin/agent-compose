package sandboxinit

import "time"

// Version is the control protocol version. It is sent by the client on every
// request and checked by the server, so a daemon and a guest init from
// different builds fail with a clear message instead of misreading frames.
const Version = 1

// Request types understood by the init control socket.
const (
	RequestExec     = "exec"
	RequestKill     = "kill"
	RequestKillTree = "kill_tree"
	RequestList     = "list"
	RequestPing     = "ping"
	RequestShutdown = "shutdown"
	// Control frames sent by the client inside an exec session.
	RequestStdin    = "stdin"
	RequestStdinEOF = "stdin_eof"
	RequestResize   = "resize"
	RequestSignal   = "signal"
)

// Frame types emitted by the init server.
const (
	FrameStarted   = "started"
	FrameStdout    = "stdout"
	FrameStderr    = "stderr"
	FrameExit      = "exit"
	FrameError     = "error"
	FrameKilled    = "killed"
	FrameProcesses = "processes"
	FramePong      = "pong"
	FrameShutdown  = "shutdown"
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
