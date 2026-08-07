package runs

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

type RunAttachInput struct {
	Kind          RunAttachInputKind
	Mode          RunAttachMode
	Request       RunAgentRequest
	AttachStdin   bool
	TTY           bool
	Rows          uint32
	Cols          uint32
	Data          []byte
	Signal        string
	Reason        string
	Text          string
	ClientFrameID string
}

type RunAttachReceiver func() (RunAttachInput, error)
