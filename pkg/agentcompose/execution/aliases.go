package execution

import owner "agent-compose/pkg/execution"

const (
	AgentResultPrefix   = owner.AgentResultPrefix
	CellTypeAgent       = owner.CellTypeAgent
	CellTypeJavaScript  = owner.CellTypeJavaScript
	CellTypePython      = owner.CellTypePython
	CellTypeShell       = owner.CellTypeShell
	CommandResultPrefix = owner.CommandResultPrefix
)

type (
	AgentExecutionStream  = owner.AgentExecutionStream
	CellExecutionStream   = owner.CellExecutionStream
	ExecStreamAccumulator = owner.ExecStreamAccumulator
	ExecuteAgentRequest   = owner.ExecuteAgentRequest
)

var (
	AgentSessionJSONLRoots             = owner.AgentSessionJSONLRoots
	CellExecSpec                       = owner.CellExecSpec
	CollectAgentResumeInfo             = owner.CollectAgentResumeInfo
	ExecContext                        = owner.ExecContext
	FindAgentSessionJSONLPaths         = owner.FindAgentSessionJSONLPaths
	FirstNonZeroInt                    = owner.FirstNonZeroInt
	FromDriverExecResult               = owner.FromDriverExecResult
	FromDriverProxyState               = owner.FromDriverProxyState
	FromDriverSessionVMInfo            = owner.FromDriverSessionVMInfo
	FromDriverVMState                  = owner.FromDriverVMState
	HostSessionDir                     = owner.HostSessionDir
	HostSessionHome                    = owner.HostSessionHome
	LoadStoredAgentSessionID           = owner.LoadStoredAgentSessionID
	MergeExecResults                   = owner.MergeExecResults
	NormalizeCellType                  = owner.NormalizeCellType
	ParseAgentExecResult               = owner.ParseAgentExecResult
	ParseCommandExecResult             = owner.ParseCommandExecResult
	RecoverExecResultFromCellArtifacts = owner.RecoverExecResultFromCellArtifacts
	SanitizeAgentExecResult            = owner.SanitizeAgentExecResult
	ShellQuote                         = owner.ShellQuote
	ShouldIncludeAgentJSONL            = owner.ShouldIncludeAgentJSONL
	StripAgentResultPayload            = owner.StripAgentResultPayload
	SummarizeAgentExecFailure          = owner.SummarizeAgentExecFailure
	ToDriverExecSpec                   = owner.ToDriverExecSpec
	ToDriverProxyState                 = owner.ToDriverProxyState
	ToDriverSession                    = owner.ToDriverSession
	ToDriverVMState                    = owner.ToDriverVMState
	WriteAgentSessionArtifact          = owner.WriteAgentSessionArtifact
	WriteCellArtifacts                 = owner.WriteCellArtifacts
	WriteJSONArtifact                  = owner.WriteJSONArtifact
)
