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
// connection catalog it resolves against, the facade token it mints, and the
// connection it derives from an agent's own declaration.
type AgentLLMStore interface {
	CatalogStore
	SaveLLMFacadeToken(ctx context.Context, token FacadeToken) error
	// UpsertDeclaredConnection persists an upstream an agent declared in its own
	// environment as a run-scoped connection. Persisting it is what lets the
	// proxy resolve the connection by id at request time, so the declared
	// credential never has to reach the guest.
	UpsertDeclaredConnection(ctx context.Context, provider Provider) error
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
	// AgentEnv is the environment the agent declared for itself. A first-party
	// LLM credential it publishes becomes a daemon-owned connection, so the run
	// is proxied and the credential stays on the daemon.
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
	// route.
	Endpoint string
	// Credential is what the guest presents at Endpoint: a run-scoped facade
	// token. The daemon never exports an upstream credential to the guest.
	Credential string
	Env        map[string]string
}

// PrepareAgentLLM is the single entry point that turns configured connections
// into the guest-facing LLM configuration of one agent run.
//
// It makes every LLM decision in one place: which model, which connection,
// which inbound protocol the agent needs, and whether that implies protocol
// conversion. The guest resolves nothing; it receives an endpoint, a credential,
// and an already-composed model string.
//
// A first-party credential an agent declares in its own environment does not
// make the agent the owner of the upstream. The declaration is imported into the
// daemon's connection configuration and the run is served through the facade
// like any other managed run, so the upstream key stays on the daemon and only a
// run-scoped token reaches the sandbox. This is what makes publishing a key in
// project or agent environment safe; the alternative — handing the key to the
// guest — is exactly the exposure the daemon exists to prevent.
//
// A catalog with no model to apply returns ErrNoModel, which callers treat as
// "the agent manages its own authentication". Every other failure is a real
// configuration error: an unreachable daemon URL, an unknown connection, or an
// upstream protocol that cannot be served to this agent.
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
	declared, hasDeclared := DeclaredUpstreamFromAgentEnv(req.Sandbox.Summary.ID, req.AgentEnv, dialect, req.Model)
	if hasDeclared && declared.Model == "" {
		// A declaration that names no model has nothing to serve: guessing the
		// catalog default would send an arbitrary model to the endpoint the agent
		// named. Report it before persisting so a run that cannot use the
		// credential does not leave it in the daemon's configuration.
		return nil, ErrNoModel
	}
	if hasDeclared {
		if err := req.Store.UpsertDeclaredConnection(ctx, declared.Provider); err != nil {
			return nil, err
		}
	}
	catalog, err := LoadCatalog(ctx, req.Store)
	if err != nil {
		return nil, err
	}
	var model string
	connectionID := ""
	if hasDeclared {
		connectionID = declared.Provider.ID
		model = declared.Model
	} else {
		// Select the model before checking anything the facade needs, so an agent
		// the daemon does not manage reports ErrNoModel even when this daemon has
		// no sandbox-reachable URL. Such an agent keeps its own endpoint and
		// credential, and a missing daemon URL is not its problem.
		model, err = catalog.SelectModel(req.Model)
		if err != nil {
			return nil, err
		}
	}
	baseURL := GuestRuntimeBaseURL(req.Config, req.Sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("agent %q needs a daemon URL reachable from the sandbox; configure %s", dialect.Kind, RuntimeBaseURLEnvName), nil)
	}
	target, err := catalog.Resolve(connectionID, model, dialect.PreferredProtocols())
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
	// Write the guest configuration before persisting the token. A writer can
	// fail (an unsupported dialect/provider package, an unwritable sandbox home),
	// and if the token were already stored that failure would orphan a live
	// credential for a run that never starts. The reverse order leaves at worst a
	// stale config file, which the next run overwrites.
	env, err := writeDialectGuestConfig(req.Config, req.Sandbox, prepared)
	if err != nil {
		return nil, err
	}
	if err := req.Store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	prepared.Env = env
	return prepared, nil
}
