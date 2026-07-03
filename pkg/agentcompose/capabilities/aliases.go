package capabilities

import owner "agent-compose/pkg/capabilities"

const (
	CapsetTagName       = owner.CapsetTagName
	ProxyTargetEnvName  = owner.ProxyTargetEnvName
	SessionTokenEnvName = owner.SessionTokenEnvName
)

type (
	DynamicProvider = owner.DynamicProvider
	GatewaySource   = owner.GatewaySource
	Provider        = owner.Provider
)

var (
	BuildGatewaySessionVars = owner.BuildGatewaySessionVars
	DecodeCapsetIDs         = owner.DecodeCapsetIDs
	EncodeCapsetIDs         = owner.EncodeCapsetIDs
	GuidePreamble           = owner.GuidePreamble
	NewDynamicProvider      = owner.NewDynamicProvider
	NormalizeCapsetIDs      = owner.NormalizeCapsetIDs
	ProxyTarget             = owner.ProxyTarget
	SessionCapsets          = owner.SessionCapsets
	SessionGuidePath        = owner.SessionGuidePath
	SessionRuntimeDir       = owner.SessionRuntimeDir
	SessionToken            = owner.SessionToken
)
