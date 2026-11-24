package runlog

// mergerCheckpoint retains inputs that have not produced a log entry yet.
// Flushing on every journal record would turn small provider deltas into one
// log line each, and still lose tools announced before their arguments arrive.
type mergerCheckpoint struct {
	PendingText     string                    `json:"pending_text,omitempty"`
	TurnProse       string                    `json:"turn_prose,omitempty"`
	TurnProseLen    int64                     `json:"turn_prose_len,omitempty"`
	LegacyOutput    string                    `json:"legacy_output,omitempty"`
	LegacyTruncated bool                      `json:"legacy_truncated,omitempty"`
	TurnEvents      bool                      `json:"turn_events,omitempty"`
	Turn            uint32                    `json:"turn,omitempty"`
	Tools           map[string]toolCheckpoint `json:"tools,omitempty"`
}

type toolCheckpoint struct {
	Tool          Tool `json:"tool"`
	CallWritten   bool `json:"call_written,omitempty"`
	ResultWritten bool `json:"result_written,omitempty"`
}

func (m *Merger) checkpoint() *mergerCheckpoint {
	state := &mergerCheckpoint{PendingText: m.pendingText.String(), TurnProse: m.turnProse,
		TurnProseLen: m.turnProseLen, LegacyOutput: m.legacyOutput, LegacyTruncated: m.legacyTruncated,
		TurnEvents: m.turnEvents, Turn: m.turn,
		Tools: make(map[string]toolCheckpoint, len(m.tools))}
	for id, tool := range m.tools {
		state.Tools[id] = toolCheckpoint{Tool: tool.tool, CallWritten: tool.callWritten, ResultWritten: tool.resultWritten}
	}
	return state
}

func (m *Merger) restoreCheckpoint(state *mergerCheckpoint) {
	m.pendingText.Reset()
	m.pendingText.WriteString(state.PendingText)
	// A checkpoint from an older daemon may hold a longer prefix.
	// It also carries no length, which is then that of the prose it holds.
	m.turnProse = keepTail(state.TurnProse, maxTurnProseBytes)
	m.turnProseLen = max(state.TurnProseLen, int64(len(state.TurnProse)))
	m.turnEvents, m.turn = state.TurnEvents, state.Turn
	// A checkpoint from an older daemon may hold a longer prefix.
	m.forgetLegacyOutput()
	if !state.TurnEvents {
		m.legacyOutput = keepTail(state.LegacyOutput, maxLegacyOutputBytes)
		m.legacyTruncated = state.LegacyTruncated || len(state.LegacyOutput) > maxLegacyOutputBytes
	}
	m.tools = make(map[string]*toolState, len(state.Tools))
	for id, tool := range state.Tools {
		saved := tool.Tool
		if tool.CallWritten {
			saved = writtenCall(saved)
		}
		m.tools[id] = &toolState{tool: saved, callWritten: tool.CallWritten, resultWritten: tool.ResultWritten}
	}
}
