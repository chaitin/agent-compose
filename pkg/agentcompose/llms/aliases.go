package llms

import owner "agent-compose/pkg/llms"

const (
	APIProtocolChatCompletions = owner.APIProtocolChatCompletions
	APIProtocolMessages        = owner.APIProtocolMessages
	APIProtocolResponses       = owner.APIProtocolResponses
	ProviderFamilyAnthropic    = owner.ProviderFamilyAnthropic
	ProviderFamilyOpenAI       = owner.ProviderFamilyOpenAI
	ProviderIDDefaultAnthropic = owner.ProviderIDDefaultAnthropic
	ProviderIDDefaultOpenAI    = owner.ProviderIDDefaultOpenAI
	ProviderScopeEnvDefault    = owner.ProviderScopeEnvDefault
	ProviderScopeSessionEnv    = owner.ProviderScopeSessionEnv
	ProviderScopeSystem        = owner.ProviderScopeSystem
)

type (
	ClientConfig              = owner.ClientConfig
	DefaultConfigStore        = owner.DefaultConfigStore
	EnvProviderLookup         = owner.EnvProviderLookup
	FacadeToken               = owner.FacadeToken
	GenerateRequest           = owner.GenerateRequest
	GenerateResult            = owner.GenerateResult
	GlobalEnvStore            = owner.GlobalEnvStore
	Model                     = owner.Model
	Provider                  = owner.Provider
	ProviderListStore         = owner.ProviderListStore
	ProviderModelWireAPIStore = owner.ProviderModelWireAPIStore
	ResolvedTarget            = owner.ResolvedTarget
)

var (
	AnthropicProviderAuthFromLookup         = owner.AnthropicProviderAuthFromLookup
	AppendAPIEndpointToBaseURL              = owner.AppendAPIEndpointToBaseURL
	ApplyForwardHeaders                     = owner.ApplyForwardHeaders
	BearerToken                             = owner.BearerToken
	ChooseSessionEnvProviderID              = owner.ChooseSessionEnvProviderID
	CopyRuntimeHeaders                      = owner.CopyRuntimeHeaders
	CopyRuntimeResponseBody                 = owner.CopyRuntimeResponseBody
	CopyRuntimeResponseHeaders              = owner.CopyRuntimeResponseHeaders
	EndpointForProvider                     = owner.EndpointForProvider
	EnsureAnthropicEnvProvider              = owner.EnsureAnthropicEnvProvider
	EnsureOpenAIEnvProvider                 = owner.EnsureOpenAIEnvProvider
	EnvHasProviderKeyForFamily              = owner.EnvHasProviderKeyForFamily
	EnvItemValue                            = owner.EnvItemValue
	EnvItemsFromMap                         = owner.EnvItemsFromMap
	FilterPersistedRuntimeEnv               = owner.FilterPersistedRuntimeEnv
	ForbiddenProviderHeader                 = owner.ForbiddenProviderHeader
	ForbiddenRuntimeHeader                  = owner.ForbiddenRuntimeHeader
	ForbiddenRuntimeResponseHeader          = owner.ForbiddenRuntimeResponseHeader
	Generate                                = owner.Generate
	HasAnthropicEnvProviderInput            = owner.HasAnthropicEnvProviderInput
	HasConfiguredProviderForFamily          = owner.HasConfiguredProviderForFamily
	HasEnabledProviderID                    = owner.HasEnabledProviderID
	HasOpenAIEnvProviderInput               = owner.HasOpenAIEnvProviderInput
	HasSessionEnvProviderInput              = owner.HasSessionEnvProviderInput
	HashFacadeToken                         = owner.HashFacadeToken
	IsSessionEnvProviderID                  = owner.IsSessionEnvProviderID
	LoaderCommandFacadeAgentModel           = owner.LoaderCommandFacadeAgentModel
	LooksLikeAnthropicMessagesEndpoint      = owner.LooksLikeAnthropicMessagesEndpoint
	LookupGlobalEnv                         = owner.LookupGlobalEnv
	ManagedRuntimeEnvMap                    = owner.ManagedRuntimeEnvMap
	MergeManagedExecEnv                     = owner.MergeManagedExecEnv
	NewFacadeToken                          = owner.NewFacadeToken
	NormalizeAPIBaseURL                     = owner.NormalizeAPIBaseURL
	NormalizeAPIEndpoint                    = owner.NormalizeAPIEndpoint
	NormalizeAPIEndpointForProtocol         = owner.NormalizeAPIEndpointForProtocol
	NormalizeAnthropicAPIBaseURL            = owner.NormalizeAnthropicAPIBaseURL
	NormalizeDefaultConfig                  = owner.NormalizeDefaultConfig
	NormalizeOptionalProviderType           = owner.NormalizeOptionalProviderType
	NormalizeProviderType                   = owner.NormalizeProviderType
	NormalizeWireAPI                        = owner.NormalizeWireAPI
	ProtocolAdapter                         = owner.ProtocolAdapter
	ProtocolFamily                          = owner.ProtocolFamily
	ProtocolsShareFamily                    = owner.ProtocolsShareFamily
	ProviderForwardHeaders                  = owner.ProviderForwardHeaders
	ProviderKeyName                         = owner.ProviderKeyName
	ProviderScopeIsConfigured               = owner.ProviderScopeIsConfigured
	ProviderSelectionPriority               = owner.ProviderSelectionPriority
	ReadRawSSEEvents                        = owner.ReadRawSSEEvents
	ResolveEndpoint                         = owner.ResolveEndpoint
	ResolveEndpointForProtocol              = owner.ResolveEndpointForProtocol
	ResolveProtocol                         = owner.ResolveProtocol
	ResolveSetting                          = owner.ResolveSetting
	RuntimeEnvMap                           = owner.RuntimeEnvMap
	RuntimeFacadeToken                      = owner.RuntimeFacadeToken
	RuntimeHeaderNameContainsSensitiveToken = owner.RuntimeHeaderNameContainsSensitiveToken
	RuntimeResponseShouldFlush              = owner.RuntimeResponseShouldFlush
	ScanFacadeToken                         = owner.ScanFacadeToken
	ScanModel                               = owner.ScanModel
	ScanProvider                            = owner.ScanProvider
	SelectModel                             = owner.SelectModel
	SelectModelAndProvider                  = owner.SelectModelAndProvider
	SelectProviderForModel                  = owner.SelectProviderForModel
	SessionAnthropicEnvModel                = owner.SessionAnthropicEnvModel
	SessionEnvProviderID                    = owner.SessionEnvProviderID
	SplitOpenCodeModel                      = owner.SplitOpenCodeModel
	UpstreamProtocolAndEndpoint             = owner.UpstreamProtocolAndEndpoint
	UseGenericResponsesTextParts            = owner.UseGenericResponsesTextParts
	WriteRawSSEEvent                        = owner.WriteRawSSEEvent
)
