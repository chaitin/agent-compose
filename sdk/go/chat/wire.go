package chat

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// The wire types below mirror the daemon's Connect JSON shapes. They stay
// unexported: every one of them is translated before it reaches a caller.

const (
	attachModePrompt      = "ATTACH_RUN_MODE_PROMPT"
	attachPolicyDetach    = "ATTACH_DISCONNECT_POLICY_DETACH"
	cleanupPolicyKeepLive = "RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING"
)

type wireRunRequest struct {
	ProjectID       string            `json:"projectId"`
	AgentName       string            `json:"agentName"`
	Prompt          string            `json:"prompt,omitempty"`
	ClientRequestID string            `json:"clientRequestId,omitempty"`
	CleanupPolicy   string            `json:"cleanupPolicy,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
}

type wireAttachStart struct {
	Request          *wireRunRequest `json:"request,omitempty"`
	Mode             string          `json:"mode,omitempty"`
	AttachStdin      bool            `json:"attachStdin,omitempty"`
	RunID            string          `json:"runId,omitempty"`
	DisconnectPolicy string          `json:"disconnectPolicy,omitempty"`
}

type wireHumanMessage struct {
	Text string `json:"text"`
}

type wireAttachRequest struct {
	Start         *wireAttachStart  `json:"start,omitempty"`
	HumanMessage  *wireHumanMessage `json:"humanMessage,omitempty"`
	Cancel        *struct{}         `json:"cancel,omitempty"`
	StdinEOF      *struct{}         `json:"stdinEof,omitempty"`
	ClientFrameID string            `json:"clientFrameId,omitempty"`
}

type wireStarted struct {
	RunID     string          `json:"runId"`
	SandboxID string          `json:"sandboxId"`
	Run       *wireRunSummary `json:"run"`
}

type wireAgentEvent struct {
	Name        string    `json:"name"`
	Text        string    `json:"text"`
	PayloadJSON string    `json:"payloadJson"`
	CreatedAt   time.Time `json:"createdAt"`
}

type wireTurnCompleted struct {
	RunID      string `json:"runId"`
	ResultJSON string `json:"resultJson"`
}

type wireAttachResult struct {
	Success    bool            `json:"success"`
	Run        *wireRunSummary `json:"run"`
	Output     string          `json:"output"`
	ResultJSON string          `json:"resultJson"`
	Error      string          `json:"error"`
}

type wireAttachError struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Terminal bool   `json:"terminal"`
}

type wireAttachResponse struct {
	Started      *wireStarted       `json:"started,omitempty"`
	AgentEvent   *wireAgentEvent    `json:"agentEvent,omitempty"`
	TurnComplete *wireTurnCompleted `json:"agentTurnCompleted,omitempty"`
	Result       *wireAttachResult  `json:"result,omitempty"`
	Error        *wireAttachError   `json:"error,omitempty"`
	CreatedAt    time.Time          `json:"createdAt"`
}

type wireRunSummary struct {
	RunID     string            `json:"runId"`
	Status    string            `json:"status"`
	SandboxID string            `json:"sandboxId"`
	Labels    map[string]string `json:"labels"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// live reports whether a run can still accept an attach.
func (w wireRunSummary) live() bool {
	status := strings.ToUpper(strings.TrimSpace(w.Status))
	return status == "RUN_STATUS_PENDING" || status == "RUN_STATUS_RUNNING"
}

type wireListRunsRequest struct {
	ProjectID string            `json:"projectId,omitempty"`
	AgentName string            `json:"agentName,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Offset    uint32            `json:"offset,omitempty"`
	Limit     uint32            `json:"limit,omitempty"`
}

type wireListRunsResponse struct {
	Runs  []wireRunSummary `json:"runs"`
	Total uint32           `json:"total"`
}

type wireListEventsRequest struct {
	RunID  string `json:"runId"`
	Offset uint32 `json:"offset,omitempty"`
	Limit  uint32 `json:"limit,omitempty"`
}

type wireRunEvent struct {
	ID        string     `json:"id"`
	Sequence  wireUint64 `json:"seq"`
	Kind      string     `json:"kind"`
	Text      string     `json:"text"`
	CreatedAt time.Time  `json:"createdAt"`
}

type wireListEventsResponse struct {
	Events []wireRunEvent `json:"events"`
	Total  uint32         `json:"total"`
}

type wireStopRunRequest struct {
	RunID  string `json:"runId"`
	Reason string `json:"reason,omitempty"`
}

// wireUint64 decodes a protobuf uint64, which Connect JSON may render as
// either a number or a string.
type wireUint64 uint64

func (v *wireUint64) UnmarshalJSON(data []byte) error {
	text := string(data)
	if len(data) > 0 && data[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
	}
	if text == "" || text == "null" {
		*v = 0
		return nil
	}
	parsed, err := strconv.ParseUint(text, 10, 64)
	if err != nil {
		return err
	}
	*v = wireUint64(parsed)
	return nil
}
