package api

import (
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// RunInputToProto exposes evidence, never temporary or host filesystem paths.
func RunInputToProto(input *domain.RunInputPreparation) *agentcomposev2.RunInputPreparation {
	if input == nil {
		return nil
	}
	state := runInputStateToProto(input.State)
	// A still-in-progress delivery whose deadline has passed is as ambiguous as
	// an unknown one: the caller must reconcile, not assume success.
	if (input.State == domain.RunInputStateReceiving || input.State == domain.RunInputStatePublishing) && !time.Now().Before(input.Deadline) {
		state = agentcomposev2.RunInputState_RUN_INPUT_STATE_UNKNOWN
	}
	result := &agentcomposev2.RunInputPreparation{RunId: input.RunID, SandboxId: input.SandboxID, State: state, ManifestSha256: input.ManifestSHA256, Deadline: FormatProjectTime(input.Deadline)}
	for _, file := range input.Files {
		path := ""
		if state == agentcomposev2.RunInputState_RUN_INPUT_STATE_READY {
			path = file.RelativePath
		}
		result.Files = append(result.Files, &agentcomposev2.RunInputFileResult{File: &agentcomposev2.RunInputFile{FileId: file.ID, DisplayName: file.Name, SizeBytes: file.Size, Sha256: file.SHA256}, RelativePath: path})
	}
	if input.FailureReason != 0 {
		result.Failure = &agentcomposev2.OperationErrorDetail{Reason: agentcomposev2.OperationErrorReason(input.FailureReason), Effect: agentcomposev2.OperationEffect_OPERATION_EFFECT_UNKNOWN}
	}
	return result
}

func runInputStateToProto(state domain.RunInputState) agentcomposev2.RunInputState {
	switch state {
	case domain.RunInputStateReceiving:
		return agentcomposev2.RunInputState_RUN_INPUT_STATE_RECEIVING
	case domain.RunInputStatePublishing:
		return agentcomposev2.RunInputState_RUN_INPUT_STATE_PUBLISHING
	case domain.RunInputStateReady:
		return agentcomposev2.RunInputState_RUN_INPUT_STATE_READY
	case domain.RunInputStateFailed:
		return agentcomposev2.RunInputState_RUN_INPUT_STATE_FAILED
	case domain.RunInputStateUnknown:
		return agentcomposev2.RunInputState_RUN_INPUT_STATE_UNKNOWN
	default:
		return agentcomposev2.RunInputState_RUN_INPUT_STATE_UNKNOWN
	}
}

func runFileInputCapabilities() *agentcomposev2.RunFileInputCapabilities {
	return &agentcomposev2.RunFileInputCapabilities{
		ProtocolVersion: 1, AttachAgentRun: true, StartAgentRun: false,
		Drivers:        []string{"docker", "boxlite", "microsandbox"},
		WorkspaceModes: []agentcomposev2.WorkspaceMode{agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY},
		MaxFiles:       runs.MaxInputFiles, MaxFileBytes: runs.MaxInputFileBytes,
		MaxTotalBytes: runs.MaxInputTotalBytes, MaxChunkBytes: runs.MaxInputChunkBytes,
		PreparationTimeoutMs: uint32(runs.InputPreparationTimeout / time.Millisecond),
	}
}
