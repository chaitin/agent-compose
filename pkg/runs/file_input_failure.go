package runs

import (
	"context"
	"errors"
	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type runInputFailure struct {
	Run     domain.ProjectRunRecord
	Sandbox *domain.Sandbox
	Input   *domain.RunInputPreparation
	Cause   error
	Send    RunAttachSender
}

func (c *Controller) finishRunInputFailure(transitionCtx, runCtx context.Context, request runInputFailure) (domain.ProjectRunRecord, error, error) {
	run, sandbox, input, cause, send := request.Run, request.Sandbox, request.Input, request.Cause, request.Send
	previous := *input
	failed := previous
	failed.State = "failed"
	if previous.State == "ready" {
		failed.State = "ready"
	}
	if previous.State == "publishing" {
		failed.State = "unknown"
	}
	failed.FailureReason = int32(apierror.ReasonStorageUnavailable)
	if errors.Is(cause, ErrInvalidRequest) || errors.Is(cause, domain.ErrInvalidArgument) {
		failed.FailureReason = int32(apierror.ReasonInvalidRequest)
	}
	if errors.Is(cause, domain.ErrConflict) {
		failed.FailureReason = int32(apierror.ReasonPreconditionFailed)
	}
	if errors.Is(cause, domain.ErrUnsupported) {
		failed.FailureReason = int32(apierror.ReasonUnsupported)
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		failed.FailureReason = int32(apierror.ReasonOperationTimeout)
	}
	if errors.Is(cause, context.Canceled) {
		failed.FailureReason = int32(apierror.ReasonOperationCanceled)
	}
	var persistErr error
	if store, ok := c.configDB.(runInputStore); ok {
		persistErr = store.SaveRunInput(transitionCtx, previous, failed)
	}
	if persistErr != nil {
		failed.State = "unknown"
	}
	sandboxID := run.SandboxID
	if sandbox != nil {
		sandboxID = sandbox.Summary.ID
	}
	// No request can retry execution once publication was attempted; retained
	// inodes and durable intent remain owned by the original Run/Sandbox.
	finished, markErr := c.completeProjectRunError(transitionCtx, runCtx, TransitionRequest{RunID: run.RunID, SandboxID: sandboxID, ExitCode: 1, Error: "run file input preparation failed"}, cause)
	sendErr := send(RunAttachOutput{Kind: RunAttachOutputError, Run: finished, Input: &failed, Code: "input_preparation_failed", Error: "run file input preparation failed", Terminal: true})
	return finished, cause, errors.Join(persistErr, markErr, sendErr)
}
