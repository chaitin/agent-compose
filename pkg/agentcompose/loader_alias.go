package agentcompose

import (
	"context"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	loaderspkg "agent-compose/pkg/loaders"
)

const (
	LoaderRuntimeScheduler = loaderspkg.LoaderRuntimeScheduler

	LoaderTriggerKindInterval = loaderspkg.LoaderTriggerKindInterval
	LoaderTriggerKindEvent    = loaderspkg.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout  = loaderspkg.LoaderTriggerKindTimeout
	LoaderTriggerKindCron     = loaderspkg.LoaderTriggerKindCron

	LoaderSessionPolicySticky = loaderspkg.LoaderSessionPolicySticky
	LoaderSessionPolicyNew    = loaderspkg.LoaderSessionPolicyNew
	LoaderSessionPolicyReuse  = loaderspkg.LoaderSessionPolicyReuse

	LoaderConcurrencyPolicySkip     = loaderspkg.LoaderConcurrencyPolicySkip
	LoaderConcurrencyPolicyParallel = loaderspkg.LoaderConcurrencyPolicyParallel

	LoaderRunStatusRunning   = loaderspkg.LoaderRunStatusRunning
	LoaderRunStatusSucceeded = loaderspkg.LoaderRunStatusSucceeded
	LoaderRunStatusFailed    = loaderspkg.LoaderRunStatusFailed
	LoaderRunStatusSkipped   = loaderspkg.LoaderRunStatusSkipped
)

type LoaderSummary = loaderspkg.LoaderSummary
type Loader = loaderspkg.Loader
type LoaderTrigger = loaderspkg.LoaderTrigger
type LoaderRunSummary = loaderspkg.LoaderRunSummary
type LoaderEvent = loaderspkg.LoaderEvent
type LoaderBinding = loaderspkg.LoaderBinding
type LoaderAgentRequest = loaderspkg.LoaderAgentRequest
type LoaderAgentResult = loaderspkg.LoaderAgentResult
type LoaderCommandRequest = loaderspkg.LoaderCommandRequest
type LoaderCommandResult = loaderspkg.LoaderCommandResult
type LoaderLLMRequest = loaderspkg.LoaderLLMRequest
type LoaderLLMResult = loaderspkg.LoaderLLMResult
type LoaderTopicEvent = loaderspkg.LoaderTopicEvent
type LoaderHost = loaderspkg.LoaderHost
type LoaderValidationResult = loaderspkg.LoaderValidationResult
type LoaderExecutionRequest = loaderspkg.LoaderExecutionRequest
type LoaderExecutionResult = loaderspkg.LoaderExecutionResult
type LoaderEngine = loaderspkg.LoaderEngine
type QJSLoaderEngine = loaderspkg.QJSLoaderEngine
type LoaderManager = loaderspkg.LoaderManager
type LoaderService = loaderspkg.Service
type loaderRunHost = loaderspkg.LoaderRunHost
type loaderTriggerEventMetadata = loaderspkg.LoaderTriggerEventMetadata

func NewLoaderService(configDB *ConfigStore, manager *LoaderManager, bus *LoaderBus) *LoaderService {
	return loaderspkg.NewService(configDB, manager, bus)
}

func newLoaderRunHost(manager *LoaderManager, loader Loader, run *LoaderRunSummary, triggerEvent loaderTriggerEventMetadata) *loaderRunHost {
	return loaderspkg.NewLoaderRunHost(manager, loader, run, triggerEvent)
}

func loaderCronSpecJSON(expr, timezone string) (string, error) {
	return loaderspkg.LoaderCronSpecJSON(expr, timezone)
}

func marshalJSONCompact(value any) (string, error) {
	return loaderspkg.MarshalJSONCompact(value)
}

func validateLoaderPublishTopic(topic string) error {
	return loaderspkg.ValidateLoaderPublishTopic(topic)
}

func jsonObjectDocument(payloadJSON string) bool {
	return loaderspkg.JSONObjectDocument(payloadJSON)
}

func int64FromMap(values map[string]any, key string) int64 {
	return loaderspkg.Int64FromMap(values, key)
}

func loaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON string) string {
	return loaderspkg.LoaderSessionRPCLinkedSessionID(method, requestJSON, responseJSON)
}

func loaderSessionIDFromJSON(raw string) string {
	return loaderspkg.LoaderSessionIDFromJSON(raw)
}

func cellTopicPayload(sessionID string, cell NotebookCell, source string) map[string]any {
	return loaderspkg.CellTopicPayload(sessionID, cell, source)
}

func loaderCommandEventPayload(request LoaderCommandRequest, result LoaderCommandResult) map[string]any {
	return loaderspkg.LoaderCommandEventPayload(request, result)
}

func sessionTopicPayload(session *Session, source string) map[string]any {
	return loaderspkg.SessionTopicPayload(session, source)
}

func NewLoaderEngine(di do.Injector) (LoaderEngine, error) {
	return loaderspkg.NewLoaderEngine(di)
}

func NewLoaderManager(di do.Injector) (*LoaderManager, error) {
	manager, err := loaderspkg.NewManager(loaderspkg.ManagerDeps{
		Config:             do.MustInvoke[*appconfig.Config](di),
		RootCtx:            do.MustInvoke[context.Context](di),
		Store:              do.MustInvoke[*Store](di),
		ConfigDB:           do.MustInvoke[*ConfigStore](di),
		Driver:             do.MustInvoke[Driver](di),
		Executor:           loaderspkg.NewExecutor(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*Store](di), do.MustInvoke[*ConfigStore](di), do.MustInvoke[RuntimeProvider](di), do.MustInvoke[*SessionStreamBroker](di).componentBroker()),
		Images:             NewDockerImageBackend(),
		LLM:                do.MustInvoke[*LLMClient](di).componentClient(),
		CapabilityProvider: do.MustInvoke[capabilityIntegration](di),
		Bus:                do.MustInvoke[*LoaderBus](di),
		Streams:            do.MustInvoke[*SessionStreamBroker](di).componentBroker(),
		Engine:             do.MustInvoke[LoaderEngine](di),
		Sessions:           do.MustInvoke[*SessionRPCBridge](di).componentBridge(),
		Dashboard:          mustDashboardHub(di),
		ProjectAgentRunner: serviceProjectAgentRunnerFromDI(di),
	})
	if err != nil {
		return nil, err
	}
	return manager, nil
}

func mustDashboardHub(di do.Injector) *DashboardOverviewHub {
	dashboard, _ := do.Invoke[*DashboardOverviewHub](di)
	return dashboard
}
