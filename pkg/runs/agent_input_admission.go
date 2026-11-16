package runs

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/agentinput"
	"github.com/chaitin/agent-compose/pkg/capmatrix"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// projectAgentLookup is the narrow store view admission needs. Keeping it here
// lets the rule be tested without a full ControllerStore.
type projectAgentLookup interface {
	GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error)
}

// admitAgentInput normalizes and admits a multimodal turn before the run record
// exists, so a rejected request never leaves a half-created run behind.
//
// A run without input takes the legacy path untouched: the prompt is the turn,
// no provider lookup happens, and no admission rule beyond what already existed
// applies. That is what keeps existing callers and scheduled runs unchanged.
func (c *Controller) admitAgentInput(ctx context.Context, req RunAgentRequest) ([]agentinput.Part, []string, error) {
	return admitAgentInput(ctx, c.configDB, c.providerMedia, req)
}

func admitAgentInput(
	ctx context.Context,
	agents projectAgentLookup,
	providers map[string]capmatrix.ProviderCapabilities,
	req RunAgentRequest,
) ([]agentinput.Part, []string, error) {
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
	provider, err := resolveRunAgentProvider(ctx, agents, req)
	if err != nil {
		return nil, nil, err
	}
	capabilities, ok := providers[provider]
	if !ok || len(capabilities.MediaTypes) == 0 {
		return nil, nil, fmt.Errorf("%w: provider %q does not accept media input", domain.ErrUnsupported, provider)
	}
	if err := agentinput.Validate(parts, capabilities.MediaPolicy()); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	return parts, warnings, nil
}

// resolveRunAgentProvider resolves the normalized provider key of the run's
// agent. It is only called for multimodal input, so a text-only run keeps its
// existing lookup path inside preparation.
func resolveRunAgentProvider(ctx context.Context, agents projectAgentLookup, req RunAgentRequest) (string, error) {
	if agents == nil {
		return "", fmt.Errorf("config store is required")
	}
	agent, err := agents.GetProjectAgent(ctx, strings.TrimSpace(req.ProjectID), strings.TrimSpace(req.AgentName))
	if err != nil {
		return "", fmt.Errorf("%w: resolve project agent %q: %w", ErrInvalidRequest, req.AgentName, err)
	}
	return domain.NormalizeAgentKind(agent.Provider), nil
}
