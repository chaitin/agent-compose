package llms

import (
	"context"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// DshFacadeStore is the persistence surface needed to resolve a DSH model and
// issue its run-scoped runtime facade token.
type DshFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// SplitDshModel parses DSH's <llm-provider-id>/<model-name> selection (same
// format as Pi/OpenCode, see agent-compose-yaml-manual.md). An agent that
// names no model at all does not reach here: EnsureDshFacadeConfig resolves
// the daemon default instead.
func SplitDshModel(value string) (string, string, error) {
	providerID, model, ok := strings.Cut(strings.TrimSpace(value), "/")
	providerID = strings.TrimSpace(providerID)
	model = strings.TrimSpace(model)
	if !ok || providerID == "" || model == "" {
		return "", "", domain.ClassifyError(domain.ErrRequired, "dsh model must use <llm-provider-id>/<model-name>", nil)
	}
	return providerID, model, nil
}

// DshFacadeConfigRequest groups EnsureDshFacadeConfig's inputs: the
// environment to resolve against (Config/Store/Sandbox) plus the specific
// call's provider/model selection and token attribution.
type DshFacadeConfigRequest struct {
	Config  *appconfig.Config
	Store   DshFacadeStore
	Sandbox *domain.Sandbox
	Model   string
	Source  string
	RunID   string
}

// EnsureDshFacadeConfig resolves DSH's model selection and returns run-scoped
// facade credentials. An explicit <llm-provider-id>/<model-name> picks that
// pair; an absent model falls back to the daemon's default catalog entry, the
// same way codex and claude behave.
//
// The wire protocol and the facade endpoint follow the resolved provider
// rather than being fixed. The profile's llm-pi-ai route names its protocol
// per request through DSH_WIRE_API and its endpoint through LLM_API_ENDPOINT,
// so the guest speaks whatever the provider serves and the request stays on
// the proxy's passthrough path — no conversion, and none of the vendor-event
// leakage a conversion can carry. The previous adapter, llm-deepseek, could
// only speak chat completions, which is why this was unconditional before
// (see docs/design/dsh_agent_provider_design.md §4.1).
func EnsureDshFacadeConfig(ctx context.Context, req DshFacadeConfigRequest) (map[string]string, error) {
	config, store, sandbox := req.Config, req.Store, req.Sandbox
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}

	target, err := resolveDshTarget(ctx, req)
	if err != nil {
		return nil, err
	}
	piAiAPI, wireAPI, facadeBaseURL, err := dshFacadeProtocol(target, baseURL, sandbox.Summary.ID)
	if err != nil {
		return nil, err
	}
	tokenValue, token, err := NewFacadeToken(NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: target.Model.Name, ProviderID: target.Provider.ID, WireAPI: wireAPI, Source: req.Source, RunID: req.RunID,
	})
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}

	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            facadeBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            wireAPI,
		// The profile's llm-pi-ai route reads this to name its wire protocol.
		"DSH_WIRE_API":        piAiAPI,
		"DSH_MODEL":           target.Model.Name,
		"DSH_PERMISSION_MODE": "danger-full-access",
	}, nil
}

// dshFacadeProtocol maps the resolved target onto the spelling llm-pi-ai uses
// for the protocol in its route config, the facade token's wire API, and the
// facade endpoint the guest talks to.
//
// It mirrors piFacadeProtocol, because llm-pi-ai is the same pi-ai adapter:
// an Anthropic-family provider is served natively over the /llm/anthropic
// route rather than bridged down to chat completions. Two things are
// unroutable: a family that is neither Anthropic nor OpenAI, and an OpenAI
// provider declaring a wire api that is neither responses nor chat
// completions. The family check is not redundant with the wire-api switch —
// provider_type has no CHECK constraint, so an unrecognised family would
// otherwise fall through to the OpenAI branch and be pointed at
// /llm/openai/v1, turning a configuration error into a request-time failure
// further downstream in UpstreamProtocolAndEndpoint.
func dshFacadeProtocol(target ResolvedTarget, runtimeBaseURL, sandboxID string) (piAiAPI, facadeProtocol, facadeBaseURL string, err error) {
	runtimeBaseURL = strings.TrimRight(runtimeBaseURL, "/")
	family := NormalizeProviderType(target.Provider.ProviderType)
	if family == ProviderFamilyAnthropic {
		// Same base-path rule as pi: the Anthropic client appends /v1/messages
		// itself, so the facade base stays at the family root.
		return "anthropic-messages", APIProtocolMessages, runtimeBaseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/anthropic", nil
	}
	if family != ProviderFamilyOpenAI {
		return "", "", "", domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("dsh does not support llm provider family %q", target.Provider.ProviderType), nil)
	}
	openAIBaseURL := runtimeBaseURL + "/api/runtime/sandboxes/" + sandboxID + "/llm/openai/v1"
	switch NormalizeWireAPI(target.WireAPI) {
	case APIProtocolResponses:
		return "openai-responses", APIProtocolResponses, openAIBaseURL, nil
	case APIProtocolChatCompletions:
		return "openai-completions", APIProtocolChatCompletions, openAIBaseURL, nil
	default:
		return "", "", "", domain.ClassifyError(domain.ErrFailedPrecondition,
			fmt.Sprintf("dsh does not support wire api %q", target.WireAPI), nil)
	}
}

// resolveDshTarget picks the provider/model pair for this run.
//
// With no model configured it delegates to the shared default resolution
// (SelectModelAndProvider picks the catalog's default entry) exactly as
// codex does, rather than going through resolveDshFacadeTarget: that
// function dispatches on the provider id, and an empty id falls through to
// the custom-OpenAI branch, which needs a concrete provider to resolve.
// OpenAI is the preferred family for that default, matching codex; an explicit
// <llm-provider-id>/<model-name> still resolves to whichever family the
// provider belongs to, and dshFacadeProtocol routes it accordingly.
func resolveDshTarget(ctx context.Context, req DshFacadeConfigRequest) (ResolvedTarget, error) {
	config, store, sandbox := req.Config, req.Store, req.Sandbox
	if strings.TrimSpace(req.Model) == "" {
		envItems, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyOpenAI)
		if err != nil {
			return ResolvedTarget{}, err
		}
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandbox.Summary.ID, PreferredProviderFamily: ProviderFamilyOpenAI,
			RequestedModel: "", ProviderID: "", EnvItems: envItems,
		})
	}
	providerID, modelName, err := SplitDshModel(req.Model)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return resolveDshFacadeTarget(ctx, dshFacadeTargetInput{Config: config, Store: store, Sandbox: sandbox, ProviderID: providerID, Model: modelName})
}

// dshFacadeTargetInput groups resolveDshFacadeTarget's inputs.
type dshFacadeTargetInput struct {
	Config     *appconfig.Config
	Store      DshFacadeStore
	Sandbox    *domain.Sandbox
	ProviderID string
	Model      string
}

// resolveDshFacadeTarget mirrors resolvePiFacadeTarget's branch structure:
// configured provider id -> family -> custom OpenAI. Since the profile now
// drives llm-pi-ai, the Anthropic family is routable here exactly as it is for
// pi, so the family branch covers it.
func resolveDshFacadeTarget(ctx context.Context, in dshFacadeTargetInput) (ResolvedTarget, error) {
	config, store, sandbox, providerID, model := in.Config, in.Store, in.Sandbox, in.ProviderID, in.Model
	sandboxID := sandbox.Summary.ID
	envItems, err := SandboxProviderEnvItems(ctx, store, sandbox, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	if HasSessionEnvProviderInput(envItems) {
		providerID, err := ensureSessionOpenAIEnvProviderWithConfig(ctx, store, SessionEnvProviderQuery{Config: config, SessionID: sandboxID, RequestedModel: model, EnvItems: envItems})
		if err != nil {
			return ResolvedTarget{}, err
		}
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: ProviderFamilyOpenAI, RequestedModel: model, ProviderID: providerID, EnvItems: envItems,
		})
	}
	if HasEnabledLLMProviderID(ctx, store, providerID) {
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: "", RequestedModel: model, ProviderID: providerID, EnvItems: envItems,
		})
	}
	switch providerID {
	case ProviderFamilyAnthropic:
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: ProviderFamilyAnthropic, RequestedModel: model, ProviderID: "", EnvItems: envItems,
		})
	case ProviderFamilyOpenAI, ProviderIDDefaultOpenAI:
		return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
			Config: config, SessionID: sandboxID, PreferredProviderFamily: ProviderFamilyOpenAI, RequestedModel: model, ProviderID: "", EnvItems: envItems,
		})
	default:
		return resolveCustomOpenAIFacadeTarget(ctx, customOpenAIFacadeTargetRequest{
			Config:     config,
			Store:      store,
			Sandbox:    sandbox,
			ProviderID: providerID,
			Model:      model,
		})
	}
}
