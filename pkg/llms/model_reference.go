package llms

import (
	"context"
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SplitModelReference parses the agent-facing model selection shared by the
// facade agents (pi, opencode, dsh).
//
// The <connection>/<model> prefix is optional. When it is present it keeps its
// established meaning and is dispatched by the caller: a configured connection
// id, a family alias (openai/anthropic), or an env-backed custom provider.
// When it is absent the whole value is a literal model name and the caller
// resolves it against the daemon's default connection, so agent configuration
// never has to repeat routing the daemon already knows.
//
// An absent value stays valid and resolves to the daemon default. A value that
// carries a slash but leaves a side empty ("/model" or "connection/") is a typo
// rather than a literal model name, so it is rejected here instead of being
// forwarded to an upstream as a model that cannot exist. The failure is an
// invalid argument rather than a missing configuration, so an agent that may
// fall back to credentials it carries itself still reports the typo.
func SplitModelReference(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "/") {
		return "", value, nil
	}
	providerID, model, ok := SplitProviderModelReference(value)
	if !ok {
		return "", "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf(
			"llm model reference %q must not leave either side of <llm-provider-id>/<model-name> empty", value), nil)
	}
	return providerID, model, nil
}

// FacadeModelReferenceQuery groups ValidateFacadeModelReference's inputs.
type FacadeModelReferenceQuery struct {
	Config *appconfig.Config
	Store  LLMResolverStore
	// SessionID and EnvItems are the session identity and the family-scoped
	// sandbox provider environment the caller passes to
	// ResolveRuntimeLLMTargetWithEnv; the session-environment branch is evaluated
	// over exactly those items.
	SessionID string
	Model     string
	EnvItems  []domain.SandboxEnvVar
}

// ValidateFacadeModelReference rejects a `<connection>/<model>` declaration whose
// prefix names no connection the resolver would select. A facade that skips this
// check leaves the declaration whole, resolves it against the daemon's default
// connection, and forwards the connection id to the upstream as part of the
// model name, reporting a daemon configuration error as an upstream capability
// error.
//
// The condition mirrors the resolver's own boundary rather than adding a second
// opinion. refineProviderAndModelFromReference resolves the declaration through a
// session environment that publishes provider input (a gateway-issued
// `<provider>/<model>` logical name arrives that way), and otherwise keeps the
// precedence of a legacy family alias or a known connection. Only the case left
// over by those branches degrades the qualified string to a literal model, so
// only that case is rejected.
func ValidateFacadeModelReference(ctx context.Context, q FacadeModelReferenceQuery) error {
	prefix, _, ok := SplitProviderModelReference(q.Model)
	if !ok {
		return nil
	}
	switch {
	case sessionHasEnvProvider(q.SessionID, q.Model, q.EnvItems):
		return nil
	case legacyReferenceUsesDefaultEnv(prefix, defaultLLMEnvProviderLookup(ctx, q.Config, q.Store)):
		return nil
	case hasEnabledLLMProviderID(ctx, q.Store, prefix):
		return nil
	case hasConfiguredProviderID(ctx, q.Store, prefix):
		return nil
	}
	return domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider %q is not configured", prefix), nil)
}

// GuestModelReference builds the model string a guest agent addresses, in the
// provider namespace its own model configuration uses (for example
// "agent-compose/gpt-5" for pi or "anthropic/claude-sonnet-4" for opencode). An
// empty provider key returns the bare model, which is how dsh and codex address
// a model whose route already carries the provider.
//
// The daemon is the single owner of this normalization: a runtime facade
// publishes the result as GuestModelEnvName and the guest runner passes it
// through untouched. Building the reference here instead of in a guest runtime
// keeps the model the agent CLI asks for and the model the facade token was
// minted for in agreement even when the resolved model id itself contains
// slashes.
func GuestModelReference(providerKey, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	providerKey = strings.TrimSpace(providerKey)
	if providerKey == "" {
		return model
	}
	// model is an upstream literal, not an already-qualified guest reference.
	// Its own prefix may equal providerKey and must remain part of the model ID.
	return providerKey + "/" + model
}

// RuntimeModelArgument encodes a resolved guest model for the runtime command
// argument. New runtimes use GuestModelEnvName as the authoritative model.
//
// Compatibility only: old DSH runtimes strip the first argument component as a
// connection prefix. Keep a disposable prefix so they preserve the full resolved
// model, including slashes. Pi already receives its guest provider namespace;
// the other runtimes consume literal arguments. Remove this DSH encoding once
// supported guest images all consume GuestModelEnvName; do not use it for new
// model selection or upstream routing.
func RuntimeModelArgument(agent, resolvedModel string) string {
	if domain.NormalizeAgentKind(agent) == "dsh" {
		return GuestModelReference("agent-compose", resolvedModel)
	}
	return strings.TrimSpace(resolvedModel)
}
