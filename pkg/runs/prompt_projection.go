package runs

import (
	"context"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// promptAttachProjector adapts streamed runtime bytes and lifecycle effects to
// the attach use case. Turn and run decisions live in agentRunProjector.
type promptAttachProjector struct {
	projection *agentRunProjector
}

type persistentPromptAttachProjectorDeps struct {
	Run        domain.ProjectRunRecord
	Sandbox    *domain.Sandbox
	LogsPath   string
	Hub        *RunLogHub
	EventStore any
}

func newPersistentPromptAttachProjector(ctx context.Context, deps persistentPromptAttachProjectorDeps) *promptAttachProjector {
	p := &promptAttachProjector{projection: newAgentRunProjector(ctx, deps.Run, deps.Sandbox, deps.LogsPath, deps.Hub)}
	p.projection.bindStore(deps.EventStore)
	return p
}

func newPromptAttachProjector(run domain.ProjectRunRecord, sandbox *domain.Sandbox, logsPath string, hub *RunLogHub) *promptAttachProjector {
	return &promptAttachProjector{projection: newAgentRunProjector(context.Background(), run, sandbox, logsPath, hub)}
}

func (p *promptAttachProjector) Project(data []byte) ([]RunAttachOutput, *TransitionRequest, error) {
	return p.ProjectChunk(domain.ExecChunk{Text: string(data)})
}

func (p *promptAttachProjector) ProjectChunk(chunk domain.ExecChunk) ([]RunAttachOutput, *TransitionRequest, error) {
	var responses []RunAttachOutput
	var transition *TransitionRequest
	effects, err := p.projection.ProjectRuntimeOutput(chunk)
	for _, effect := range effects {
		if effect.Terminal != nil {
			transition = effect.Terminal
		}
		switch {
		case effect.FrameType == "result":
		case effect.FrameType == "error":
			responses = append(responses, runAttachErrorResponse(firstNonEmpty(effect.Code, "runtime_stream_error"), effect.Text, true))
		case effect.TurnCompleted:
			responses = append(responses, runAttachAgentTurnCompletedResponse(p.projection.run, string(effect.Payload), warningsFromRun(p.projection.run)))
		default:
			responses = append(responses, runAttachAgentEventResponse(firstNonEmpty(effect.Name, "agent_event"), effect.Text, string(effect.Payload)))
		}
	}

	return responses, transition, err
}

func (p *promptAttachProjector) AppendHumanMessage(message string) error {
	return p.AppendHumanMessageFrame(message, "")
}

func (p *promptAttachProjector) AppendHumanMessageFrame(message, clientFrameID string) error {
	return p.projection.AppendHumanMessageFrame(message, clientFrameID)
}

func (p *promptAttachProjector) AppendStderr(text string) error {
	return p.projection.AppendOutput(domain.ExecChunk{Text: text, Stream: domain.StdioStderr})
}
