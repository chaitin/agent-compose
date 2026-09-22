package llms

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// AgentLLMStore is the persistence surface PrepareAgentLLM needs: the
// connection catalog it resolves against and the facade token it mints.
type AgentLLMStore interface {
	CatalogStore
	SaveLLMFacadeToken(ctx context.Context, token FacadeToken) error
}

// AgentLLMRequest describes the managed LLM configuration of one agent run.
type AgentLLMRequest struct {
	Config    *appconfig.Config
	Store     AgentLLMStore
	Sandbox   *domain.Sandbox
	AgentKind string
	// Model is the opaque model the agent declared. Empty selects the catalog
	// default model.
	Model string
	// ConnectionID names the upstream connection explicitly. Empty infers it
	// from the model.
	ConnectionID string
	// AgentEnv is the environment the agent declared for itself. When it
	// publishes an LLM connection, that connection owns this run and the catalog
	// is not consulted at all.
	AgentEnv []domain.SandboxEnvVar
	Source   string
	RunID    string
}

// IsUnmanagedAgentLLMError reports whether err means "the daemon has no managed
// LLM configuration for this agent", as opposed to a configuration fault the
// operator must resolve.
//
// Callers that configure one agent run treat it as a no-op so the agent keeps
// its own authentication. Keeping the predicate here, rather than repeating the
// three sentinels at every call site, is what stops those call sites from
// drifting apart as the catalog gains failure modes.
func IsUnmanagedAgentLLMError(err error) bool {
	return errors.Is(err, ErrNoModel) ||
		errors.Is(err, ErrNoConnection) ||
		errors.Is(err, ErrUnsupportedAgentDialect)
}

// AgentLLM is the resolved, guest-facing LLM configuration of one run.
type AgentLLM struct {
	Dialect    Dialect
	Target     ResolvedTarget
	Model      string
	GuestModel string
	Upstream   Protocol
	Inbound    Protocol
	Convert    bool
	Token      string
	BaseURL    string
	// Endpoint is the base URL the guest is pointed at: the daemon's facade
	// route in managed mode, or the upstream the agent declared in direct mode.
	Endpoint string
	// Credential is what the guest presents at Endpoint: a run-scoped facade
	// token in managed mode, or the agent's own upstream key in direct mode.
	Credential string
	// Direct reports that the agent declared its own upstream, so the daemon
	// neither proxies nor converts this run's calls.
	Direct bool
	Env    map[string]string
}

// PrepareAgentLLM is the single entry point that turns configured connections
// into the guest-facing LLM configuration of one agent run.
//
// It makes every LLM decision in one place: whether the agent or the daemon owns
// the upstream, which model, which connection, which inbound protocol the agent
// needs, and whether that implies protocol conversion. The guest resolves
// nothing; it receives an endpoint, a credential, and an already-composed model
// string.
//
// The two modes are mutually exclusive. An agent that declares its own LLM
// connection in its environment is served by that connection and the catalog is
// not consulted; an agent that declares none is served by the catalog.
//
// A catalog with no model to apply returns ErrNoModel, which callers treat as
// "the agent manages its own authentication". Every other failure is a real
// configuration error: an ambiguous connection, an unknown model binding, an
// unreachable daemon URL, or an upstream protocol the agent cannot be served.
func PrepareAgentLLM(ctx context.Context, req AgentLLMRequest) (*AgentLLM, error) {
	if req.Store == nil {
		return nil, errors.New("llm preparation requires a catalog store")
	}
	if req.Sandbox == nil {
		return nil, errors.New("llm preparation requires a sandbox")
	}
	dialect, err := DialectFor(req.AgentKind)
	if err != nil {
		return nil, err
	}
	if upstream, declared := directUpstreamFromAgentEnv(req.AgentEnv, dialect); declared {
		return prepareDirectAgentLLM(req, dialect, upstream)
	}
	catalog, err := LoadCatalog(ctx, req.Store)
	if err != nil {
		return nil, err
	}
	// Select the model before checking anything the facade needs, so an agent
	// the daemon does not manage reports ErrNoModel even when this daemon has no
	// sandbox-reachable URL. Such an agent keeps its own endpoint and credential,
	// and a missing daemon URL is not its problem.
	model, err := catalog.SelectModel(req.Model)
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(req.Config, req.Sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("agent %q needs a daemon URL reachable from the sandbox; configure %s", dialect.Kind, RuntimeBaseURLEnvName), nil)
	}
	target, err := catalog.Resolve(req.ConnectionID, model)
	if err != nil {
		return nil, err
	}
	upstream := NormalizeProtocol(target.WireAPI)
	if !upstream.Valid() {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("llm connection %q declares unsupported protocol %q", target.Provider.ID, target.WireAPI), nil)
	}
	inbound := dialect.InboundProtocol(upstream)
	if dialect.NeedsConversion(upstream) && !CanConvert(inbound, upstream) {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("cannot serve a %s upstream to %s: no %s to %s conversion is available", upstream, dialect.Kind, inbound, upstream), nil)
	}
	guestModel := dialect.GuestModel(model)
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID:  req.Sandbox.Summary.ID,
		Model:      model,
		ProviderID: target.Provider.ID,
		WireAPI:    string(inbound),
		GuestModel: guestModel,
		Source:     req.Source,
		RunID:      req.RunID,
	})
	if err != nil {
		return nil, err
	}
	if err := req.Store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	daemonBaseURL := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	prepared := &AgentLLM{
		Dialect:    dialect,
		Target:     target,
		Model:      model,
		GuestModel: guestModel,
		Upstream:   upstream,
		Inbound:    inbound,
		Convert:    dialect.NeedsConversion(upstream),
		Token:      tokenValue,
		BaseURL:    daemonBaseURL,
		Endpoint:   facadeEndpoint(daemonBaseURL, req.Sandbox.Summary.ID, inbound),
		Credential: tokenValue,
	}
	env, err := writeDialectGuestConfig(req.Config, req.Sandbox, prepared)
	if err != nil {
		return nil, err
	}
	prepared.Env = env
	return prepared, nil
}
