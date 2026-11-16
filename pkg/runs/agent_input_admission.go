package runs

import (
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/agentinput"
)

// admitAgentInput normalizes and admits a text/file turn before the run record
// exists, so a rejected request never leaves a half-created run behind.
//
// Admission is provider-neutral: every file type is accepted and only the
// daemon-wide limits are applied. Materialization happens in the execution
// layer, which writes the files into the sandbox workspace.
func (c *Controller) admitAgentInput(req RunAgentRequest) ([]agentinput.Part, []string, error) {
	if len(req.Input) == 0 {
		return nil, nil, nil
	}
	if strings.TrimSpace(req.Command) != "" {
		return nil, nil, fmt.Errorf("%w: input cannot be combined with command", ErrInvalidRequest)
	}
	parts, warnings, err := agentinput.Normalize(req.Prompt, req.Input)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if err := agentinput.Validate(parts, c.agentInputLimits()); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return parts, warnings, nil
}

// agentInputLimits reads the daemon-wide limits once per admitted turn.
func (c *Controller) agentInputLimits() agentinput.Limits {
	limits := agentinput.DefaultLimits()
	if c.config == nil {
		return limits
	}
	if c.config.AgentInputFileLimitBytes > 0 {
		limits.MaxFileBytes = uint64(c.config.AgentInputFileLimitBytes)
	}
	if c.config.AgentInputTurnLimitBytes > 0 {
		limits.MaxTurnBytes = uint64(c.config.AgentInputTurnLimitBytes)
	}
	if c.config.AgentInputFileLimit > 0 {
		limits.MaxFiles = uint32(c.config.AgentInputFileLimit)
	}
	return limits
}
