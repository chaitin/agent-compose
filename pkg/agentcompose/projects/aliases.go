package projects

import owner "agent-compose/pkg/projects"

type (
	SchedulerBuild = owner.SchedulerBuild
)

var (
	AsInt64Time                         = owner.AsInt64Time
	EncodeSourceJSON                    = owner.EncodeSourceJSON
	JSStringLiteral                     = owner.JSStringLiteral
	ManagedAgentDefinitionUnchanged     = owner.ManagedAgentDefinitionUnchanged
	ManagedLoaderTriggerAndRegistration = owner.ManagedLoaderTriggerAndRegistration
	ManagedLoaderTriggersAndScript      = owner.ManagedLoaderTriggersAndScript
	ManagedLoaderUnchanged              = owner.ManagedLoaderUnchanged
	MarshalCanonicalJSON                = owner.MarshalCanonicalJSON
	NewAgentDefinitionFromSpec          = owner.NewAgentDefinitionFromSpec
	NewAgentDefinitionsFromSpec         = owner.NewAgentDefinitionsFromSpec
	NewAgentRecordFromSpec              = owner.NewAgentRecordFromSpec
	NewAgentRecordsFromSpec             = owner.NewAgentRecordsFromSpec
	NewManagedLoaderFromScheduler       = owner.NewManagedLoaderFromScheduler
	NewRecordFromSpec                   = owner.NewRecordFromSpec
	NewSchedulerBuildsFromSpec          = owner.NewSchedulerBuildsFromSpec
	NewSchedulerRecordFromSpec          = owner.NewSchedulerRecordFromSpec
	NormalizeAgentRecord                = owner.NormalizeAgentRecord
	NormalizeComparableLoaderTriggers   = owner.NormalizeComparableLoaderTriggers
	NormalizeRecord                     = owner.NormalizeRecord
	NormalizeRunRecord                  = owner.NormalizeRunRecord
	NormalizeRunStatus                  = owner.NormalizeRunStatus
	NormalizeRunStatusFilter            = owner.NormalizeRunStatusFilter
	NormalizeSchedulerRecord            = owner.NormalizeSchedulerRecord
	ParseInt64String                    = owner.ParseInt64String
	ProjectAgentRecordUnchanged         = owner.ProjectAgentRecordUnchanged
	ProjectRecordUnchanged              = owner.ProjectRecordUnchanged
	RecordMatchesQuery                  = owner.RecordMatchesQuery
	SameCapsetIDs                       = owner.SameCapsetIDs
	SameLoaderTriggerSpecs              = owner.SameLoaderTriggerSpecs
	SameSessionEnvItems                 = owner.SameSessionEnvItems
	ScanProject                         = owner.ScanProject
	ScanProjectAgent                    = owner.ScanProjectAgent
	ScanProjectRevision                 = owner.ScanProjectRevision
	ScanProjectRun                      = owner.ScanProjectRun
	ScanProjectScheduler                = owner.ScanProjectScheduler
	SchedulerLoaders                    = owner.SchedulerLoaders
	SchedulerRecordUnchanged            = owner.SchedulerRecordUnchanged
	SchedulerRecords                    = owner.SchedulerRecords
	SelectProjectRunSQL                 = owner.SelectProjectRunSQL
	SessionEnvItemsFromCompose          = owner.SessionEnvItemsFromCompose
)
