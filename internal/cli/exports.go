package cli

import agentcomposev2 "agent-compose/proto/agentcompose/v2"

const (
	ExitCodeGeneral     = exitCodeGeneral
	ExitCodeUsage       = exitCodeUsage
	ExitCodeUnavailable = exitCodeUnavailable
)

type (
	UpOutput               = composeUpOutput
	DownOutput             = composeDownOutput
	UpProjectOutput        = composeUpProjectOutput
	UpRevisionOutput       = composeUpRevisionOutput
	UpChangeOutput         = composeUpChangeOutput
	RunOutput              = composeRunOutput
	LogsOutput             = composeLogsOutput
	PSOutput               = composePSOutput
	PSAgentOutput          = composePSAgentOutput
	ProjectOutput          = composeProjectOutput
	ProjectAgentOutput     = composeProjectAgentOutput
	ProjectSchedulerOutput = composeProjectSchedulerOutput
	AgentInspectOutput     = composeAgentInspectOutput
	SessionOutput          = composeSessionOutput
	ExecOutput             = composeExecOutput
	ImageListOutput        = composeImageListOutput
	ImageInspectOutput     = composeImageInspectOutput
	ImagePullOutput        = composeImagePullOutput
	ImageRemoveOutput      = composeImageRemoveOutput
	ImageOutput            = composeImageOutput
	ImageStoreOutput       = composeImageStoreOutput
	ImageProgressItem      = composeImageProgressItem
)

func ImageOutputFromProto(image *agentcomposev2.Image) ImageOutput {
	return composeImageOutputFromProto(image)
}
