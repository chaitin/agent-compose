package loaders

import owner "agent-compose/pkg/loaders"

type (
	LoaderEngine           = owner.LoaderEngine
	LoaderExecutionRequest = owner.LoaderExecutionRequest
	LoaderExecutionResult  = owner.LoaderExecutionResult
	LoaderHost             = owner.LoaderHost
	LoaderValidationResult = owner.LoaderValidationResult
	QJSLoaderEngine        = owner.QJSLoaderEngine
)

var (
	CellTopicPayload               = owner.CellTopicPayload
	CommandCellSource              = owner.CommandCellSource
	CommandContext                 = owner.CommandContext
	CommandEventPayload            = owner.CommandEventPayload
	CommandRequestOverridesSession = owner.CommandRequestOverridesSession
	CommandRequestRequiresCleanup  = owner.CommandRequestRequiresCleanup
	DecodeEnvItems                 = owner.DecodeEnvItems
	EncodeEnvItems                 = owner.EncodeEnvItems
	EngineMaxExecutionTime         = owner.EngineMaxExecutionTime
	LoaderCronSpecJSON             = owner.LoaderCronSpecJSON
	LoaderTriggerNextFireAt        = owner.LoaderTriggerNextFireAt
	LoaderTriggerSource            = owner.LoaderTriggerSource
	NewLoaderEngine                = owner.NewLoaderEngine
	NormalizeLoader                = owner.NormalizeLoader
	NormalizeLoaderCronSpecJSON    = owner.NormalizeLoaderCronSpecJSON
	NormalizeLoaderTrigger         = owner.NormalizeLoaderTrigger
	ParseRunTimeout                = owner.ParseRunTimeout
	ScanLoader                     = owner.ScanLoader
	ScanLoaderBinding              = owner.ScanLoaderBinding
	ScanLoaderEvent                = owner.ScanLoaderEvent
	ScanLoaderRun                  = owner.ScanLoaderRun
	ScanLoaderSummary              = owner.ScanLoaderSummary
	ScanLoaderTrigger              = owner.ScanLoaderTrigger
	SelectLoaderBindingSQL         = owner.SelectLoaderBindingSQL
	SelectLoaderEventSQL           = owner.SelectLoaderEventSQL
	SelectLoaderRunSQL             = owner.SelectLoaderRunSQL
	SelectLoaderSQL                = owner.SelectLoaderSQL
	SelectLoaderSummarySQL         = owner.SelectLoaderSummarySQL
	SelectLoaderTriggerSQL         = owner.SelectLoaderTriggerSQL
	SessionTopicPayload            = owner.SessionTopicPayload
	ValidateCommandRequest         = owner.ValidateCommandRequest
)
