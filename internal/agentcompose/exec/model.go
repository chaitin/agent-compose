package exec

type CellType string

const (
	CellTypeShell      = "shell"
	CellTypeJavaScript = "javascript"
	CellTypePython     = "python"
	CellTypeAgent      = "agent"
)

type Chunk struct {
	Text     string
	IsStderr bool
}

type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Output   string
	Success  bool
}

type Spec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

type AgentRunResult struct {
	Agent         string
	DisplayOutput string
	FinalText     string
	JSONText      string
	Transcript    string
	Success       bool
	ExitCode      int
	SessionID     string
	StopReason    string
}

type StreamWriter func(Chunk)

type AgentTraceEvent struct {
	Type    string
	Level   string
	Message string
}
