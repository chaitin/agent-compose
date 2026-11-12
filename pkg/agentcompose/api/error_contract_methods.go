package api

import (
	agentcomposev2connect "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// rpcKinds is the exhaustive read/write classification of the agentcompose v2
// API. The interceptor needs it because the direction cannot be inferred from
// the error: only the operation decides whether an effect is meaningful.
//
// Every procedure must have an entry. TestErrorContractCoversEveryRPC walks the
// proto descriptor and fails when a new RPC is not classified, so a missing
// entry cannot silently default to a write in production.
//
// The table is read-only after initialization.
var rpcKinds = map[string]rpcKind{
	// ProjectService
	agentcomposev2connect.ProjectServiceValidateProjectProcedure:              rpcRead, // pure validation; no mutation
	agentcomposev2connect.ProjectServiceApplyProjectProcedure:                 rpcWrite,
	agentcomposev2connect.ProjectServicePatchProjectProcedure:                 rpcWrite,
	agentcomposev2connect.ProjectServiceGetProjectProcedure:                   rpcRead,
	agentcomposev2connect.ProjectServiceListProjectsProcedure:                 rpcRead,
	agentcomposev2connect.ProjectServiceRemoveProjectProcedure:                rpcWrite,
	agentcomposev2connect.ProjectServiceWatchProjectProcedure:                 rpcRead,
	agentcomposev2connect.ProjectServiceGetSchedulerProcedure:                 rpcRead,
	agentcomposev2connect.ProjectServiceListSchedulersProcedure:               rpcRead,
	agentcomposev2connect.ProjectServiceListSchedulerEventsProcedure:          rpcRead,
	agentcomposev2connect.ProjectServiceListProjectSchedulerEventsProcedure:   rpcRead,
	agentcomposev2connect.ProjectServiceStreamProjectSchedulerEventsProcedure: rpcRead, // read-only event stream
	agentcomposev2connect.ProjectServiceInvokeSchedulerProcedure:              rpcWrite,
	agentcomposev2connect.ProjectServiceRunSchedulerProcedure:                 rpcWrite,
	agentcomposev2connect.ProjectServiceStartSchedulerRunProcedure:            rpcWrite,
	agentcomposev2connect.ProjectServiceGetSchedulerRunProcedure:              rpcRead,
	agentcomposev2connect.ProjectServiceListSchedulerRunsProcedure:            rpcRead,
	agentcomposev2connect.ProjectServiceBatchGetLatestSchedulerRunsProcedure:  rpcRead,
	agentcomposev2connect.ProjectServiceStreamSchedulerRunsProcedure:          rpcRead, // read-only run stream
	agentcomposev2connect.ProjectServicePruneSchedulerRunsProcedure:           rpcWrite,
	agentcomposev2connect.ProjectServiceStopSchedulerRunProcedure:             rpcWrite,
	agentcomposev2connect.ProjectServiceSetSchedulerEnabledProcedure:          rpcWrite,
	agentcomposev2connect.ProjectServiceSetSchedulerTriggerEnabledProcedure:   rpcWrite,

	// RunService
	agentcomposev2connect.RunServiceRunAgentProcedure:             rpcWrite,
	agentcomposev2connect.RunServiceStartAgentRunProcedure:        rpcWrite,
	agentcomposev2connect.RunServiceStreamAgentRunProcedure:       rpcWrite, // creates a run and drives an agent
	agentcomposev2connect.RunServiceAttachAgentRunProcedure:       rpcWrite, // drives an interactive agent run
	agentcomposev2connect.RunServiceGetRunProcedure:               rpcRead,
	agentcomposev2connect.RunServiceListRunsProcedure:             rpcRead,
	agentcomposev2connect.RunServiceFollowRunLogsProcedure:        rpcRead,
	agentcomposev2connect.RunServiceStopRunProcedure:              rpcWrite,
	agentcomposev2connect.RunServiceAbandonRunCompletionProcedure: rpcWrite,
	agentcomposev2connect.RunServiceListRunEventsProcedure:        rpcRead,
	agentcomposev2connect.RunServiceListSandboxRunEventsProcedure: rpcRead,

	// ExecService
	agentcomposev2connect.ExecServiceExecProcedure:       rpcWrite, // executes a command that may mutate the sandbox
	agentcomposev2connect.ExecServiceStreamExecProcedure: rpcWrite, // same as Exec, streamed
	agentcomposev2connect.ExecServiceAttachExecProcedure: rpcWrite, // drives an interactive process

	// ImageService
	agentcomposev2connect.ImageServiceListImagesProcedure:   rpcRead,
	agentcomposev2connect.ImageServicePullImageProcedure:    rpcWrite,
	agentcomposev2connect.ImageServiceInspectImageProcedure: rpcRead,
	agentcomposev2connect.ImageServiceRemoveImageProcedure:  rpcWrite,
	agentcomposev2connect.ImageServiceBuildImageProcedure:   rpcWrite,

	// CacheService
	agentcomposev2connect.CacheServiceListCachesProcedure:   rpcRead,
	agentcomposev2connect.CacheServiceInspectCacheProcedure: rpcRead,
	agentcomposev2connect.CacheServicePruneCachesProcedure:  rpcWrite,
	agentcomposev2connect.CacheServiceRemoveCacheProcedure:  rpcWrite,

	// VolumeService
	agentcomposev2connect.VolumeServiceListVolumesProcedure:   rpcRead,
	agentcomposev2connect.VolumeServiceCreateVolumeProcedure:  rpcWrite,
	agentcomposev2connect.VolumeServiceInspectVolumeProcedure: rpcRead,
	agentcomposev2connect.VolumeServiceRemoveVolumeProcedure:  rpcWrite,
	agentcomposev2connect.VolumeServicePruneVolumesProcedure:  rpcWrite,

	// SandboxService
	agentcomposev2connect.SandboxServiceRemoveSandboxProcedure:                rpcWrite,
	agentcomposev2connect.SandboxServicePruneSandboxesProcedure:               rpcWrite,
	agentcomposev2connect.SandboxServiceGetSandboxStatsProcedure:              rpcRead,
	agentcomposev2connect.SandboxServiceGetSandboxProcedure:                   rpcRead,
	agentcomposev2connect.SandboxServiceStopSandboxProcedure:                  rpcWrite,
	agentcomposev2connect.SandboxServiceResumeSandboxProcedure:                rpcWrite,
	agentcomposev2connect.SandboxServiceListSandboxesProcedure:                rpcRead,
	agentcomposev2connect.SandboxServiceListSandboxHistoryProcedure:           rpcRead,
	agentcomposev2connect.SandboxServiceWatchSandboxProcedure:                 rpcRead,
	agentcomposev2connect.SandboxServiceListSandboxWorkspaceProcedure:         rpcRead,
	agentcomposev2connect.SandboxServiceDownloadSandboxWorkspaceFileProcedure: rpcRead,

	// DashboardService
	agentcomposev2connect.DashboardServiceGetDashboardOverviewProcedure:   rpcRead,
	agentcomposev2connect.DashboardServiceWatchDashboardOverviewProcedure: rpcRead,

	// SettingsService
	agentcomposev2connect.SettingsServiceGetGlobalEnvProcedure:                  rpcRead,
	agentcomposev2connect.SettingsServiceUpdateGlobalEnvProcedure:               rpcWrite,
	agentcomposev2connect.SettingsServiceGetCapabilityGatewayConfigProcedure:    rpcRead,
	agentcomposev2connect.SettingsServiceUpdateCapabilityGatewayConfigProcedure: rpcWrite,
	agentcomposev2connect.SettingsServiceGetEventDeliveryScopeProcedure:         rpcRead,
	agentcomposev2connect.SettingsServiceListWorkspacePresetsProcedure:          rpcRead,
	agentcomposev2connect.SettingsServiceCreateWorkspacePresetProcedure:         rpcWrite,
	agentcomposev2connect.SettingsServiceUpdateWorkspacePresetProcedure:         rpcWrite,
	agentcomposev2connect.SettingsServiceDeleteWorkspacePresetProcedure:         rpcWrite,

	// CapabilityService
	agentcomposev2connect.CapabilityServiceGetCapabilityStatusProcedure:  rpcRead,
	agentcomposev2connect.CapabilityServiceListCapabilitySetsProcedure:   rpcRead,
	agentcomposev2connect.CapabilityServiceGetCapabilityCatalogProcedure: rpcRead,

	// LLMService
	agentcomposev2connect.LLMServiceCreateProviderProcedure:    rpcWrite,
	agentcomposev2connect.LLMServiceGetProviderProcedure:       rpcRead,
	agentcomposev2connect.LLMServiceListProvidersProcedure:     rpcRead,
	agentcomposev2connect.LLMServiceUpdateProviderProcedure:    rpcWrite,
	agentcomposev2connect.LLMServiceDeleteProviderProcedure:    rpcWrite,
	agentcomposev2connect.LLMServiceGetDefaultModelProcedure:   rpcRead,
	agentcomposev2connect.LLMServiceSetDefaultModelProcedure:   rpcWrite,
	agentcomposev2connect.LLMServiceClearDefaultModelProcedure: rpcWrite,
	agentcomposev2connect.LLMServiceGenerateProcedure:          rpcRead, // stateless upstream call; no daemon state changes

	// ResourceService
	agentcomposev2connect.ResourceServiceResolveIDProcedure: rpcRead,

	// EngineService
	agentcomposev2connect.EngineServiceGetCapabilitiesProcedure: rpcRead,
}
