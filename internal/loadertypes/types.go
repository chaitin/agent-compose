package loadertypes

import (
	modeldomain "agent-compose/internal/model"
	"time"
)

type SessionEnvVar = modeldomain.SessionEnvVar

type LoaderAgentRequest struct {
	Agent         string          `json:"agent,omitempty"`
	SessionPolicy string          `json:"sessionPolicy,omitempty"`
	Timeout       time.Duration   `json:"timeout,omitempty"`
	Title         string          `json:"title,omitempty"`
	Driver        string          `json:"driver,omitempty"`
	GuestImage    string          `json:"guestImage,omitempty"`
	WorkspaceID   string          `json:"workspaceId,omitempty"`
	SessionEnv    []SessionEnvVar `json:"sessionEnv,omitempty"`
	OutputSchema  string          `json:"outputSchema,omitempty"`
}

type LoaderAgentResult struct {
	Text           string `json:"text,omitempty"`
	Output         string `json:"output,omitempty"`
	FinalText      string `json:"finalText,omitempty"`
	JSON           any    `json:"json"`
	SessionID      string `json:"sessionId,omitempty"`
	CellID         string `json:"cellId,omitempty"`
	Agent          string `json:"agent,omitempty"`
	AgentSessionID string `json:"agentSessionId,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	Success        bool   `json:"success"`
	ExitCode       int    `json:"exitCode"`
}

type LoaderCommandRequest struct {
	Mode           string            `json:"mode"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Script         string            `json:"script,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMs      int64             `json:"timeoutMs,omitempty"`
	MaxOutputBytes int64             `json:"maxOutputBytes,omitempty"`
	SessionPolicy  string            `json:"sessionPolicy,omitempty"`
	Title          string            `json:"title,omitempty"`
	Driver         string            `json:"driver,omitempty"`
	GuestImage     string            `json:"guestImage,omitempty"`
	WorkspaceID    string            `json:"workspaceId,omitempty"`
	SessionEnv     []SessionEnvVar   `json:"sessionEnv,omitempty"`
}

type LoaderCommandResult struct {
	Stdout          string            `json:"stdout"`
	Stderr          string            `json:"stderr"`
	Output          string            `json:"output"`
	ExitCode        int               `json:"exitCode"`
	Success         bool              `json:"success"`
	StdoutTruncated bool              `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool              `json:"stderrTruncated,omitempty"`
	OutputTruncated bool              `json:"outputTruncated,omitempty"`
	SessionID       string            `json:"sessionId,omitempty"`
	CellID          string            `json:"cellId,omitempty"`
	Artifacts       map[string]string `json:"artifacts,omitempty"`
}

type LoaderLLMRequest struct {
	Model        string `json:"model,omitempty"`
	OutputSchema string `json:"outputSchema,omitempty"`
}

type LoaderLLMResult struct {
	Text         string `json:"text,omitempty"`
	Model        string `json:"model,omitempty"`
	ResponseID   string `json:"responseId,omitempty"`
	FinishReason string `json:"finishReason,omitempty"`
	JSON         any    `json:"json"`
}
