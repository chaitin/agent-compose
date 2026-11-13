package runs

type RunAttachInputKind string

const (
	RunAttachInputStart              RunAttachInputKind = "start"
	RunAttachInputFileChunk          RunAttachInputKind = "file_chunk"
	RunAttachInputComplete           RunAttachInputKind = "input_complete"
	RunAttachInputUploadDisconnected RunAttachInputKind = "upload_disconnected"
	RunAttachInputStdin              RunAttachInputKind = "stdin"
	RunAttachInputStdinEOF           RunAttachInputKind = "stdin_eof"
	RunAttachInputResize             RunAttachInputKind = "resize"
	RunAttachInputSignal             RunAttachInputKind = "signal"
	RunAttachInputCancel             RunAttachInputKind = "cancel"
	RunAttachInputHumanMessage       RunAttachInputKind = "human_message"
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
	FileID           string
	Offset           uint64
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
	ClientFrameID    string
}

type RunAttachReceiver func() (RunAttachInput, error)
