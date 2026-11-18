package runs

import "github.com/chaitin/agent-compose/pkg/agentinput"

type RunAttachInputKind string

const (
	RunAttachInputStart        RunAttachInputKind = "start"
	RunAttachInputStdin        RunAttachInputKind = "stdin"
	RunAttachInputStdinEOF     RunAttachInputKind = "stdin_eof"
	RunAttachInputResize       RunAttachInputKind = "resize"
	RunAttachInputSignal       RunAttachInputKind = "signal"
	RunAttachInputCancel       RunAttachInputKind = "cancel"
	RunAttachInputHumanMessage RunAttachInputKind = "human_message"
)

type RunAttachMode string

const (
	RunAttachModeUnspecified RunAttachMode = ""
	RunAttachModeInvalid     RunAttachMode = "invalid"
	RunAttachModeCommand     RunAttachMode = "command"
	RunAttachModePrompt      RunAttachMode = "prompt"
)

type AttachDisconnectPolicy string

const (
	AttachDisconnectCancel AttachDisconnectPolicy = "cancel"
	AttachDisconnectDetach AttachDisconnectPolicy = "detach"
)

type RunAttachInput struct {
	Kind             RunAttachInputKind
	RunID            string
	DisconnectPolicy AttachDisconnectPolicy
	Mode             RunAttachMode
	Request          RunAgentRequest
	AttachStdin      bool
	TTY              bool
	Rows             uint32
	Cols             uint32
	Data             []byte
	Signal           string
	Reason           string
	Text             string
	// Parts is the ordered text/file input of a human message. Empty keeps
	// Text as the whole message.
	Parts         []agentinput.Part
	ClientFrameID string
}

type RunAttachReceiver func() (RunAttachInput, error)
