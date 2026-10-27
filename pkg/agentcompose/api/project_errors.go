package api

import (
	"errors"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

// ConnectErrorForProject maps a project write failure. The effect comes from
// the not-applied marker so an unclassified failure reports UNKNOWN.
func ConnectErrorForProject(err error) error {
	if err == nil {
		return nil
	}
	effect := apierror.EffectFor(err)
	// Keep a detail an inner boundary already attached (it may be more precise
	// than a code-to-reason mapping), but upgrade a read-style UNSPECIFIED
	// effect: this mapper only serves write RPCs.
	if detail, ok := errdetail.From(err); ok {
		return preserveOrUpgradeDetail(err, detail, effect)
	}
	code, reason := classifyProjectError(err)
	return NewOperationError(code, reason, effect, err)
}

func classifyProjectError(err error) (connect.Code, apierror.Reason) {
	switch {
	case errors.Is(err, projects.ErrRevisionConflict):
		return connect.CodeAborted, apierror.ReasonRevisionConflict
	case errors.Is(err, projects.ErrVolumeNotFound):
		return connect.CodeNotFound, apierror.ReasonVolumeNotFound
	case errors.Is(err, projects.ErrVolumeUnavailable):
		return connect.CodeUnavailable, apierror.ReasonVolumeUnavailable
	case errors.Is(err, images.ErrImageNotFound):
		return connect.CodeUnavailable, apierror.ReasonImageNotFound
	case errors.Is(err, images.ErrPullFailed):
		return connect.CodeUnavailable, apierror.ReasonImagePullFailed
	case errors.Is(err, images.ErrInvalidReference):
		return connect.CodeInvalidArgument, apierror.ReasonImageReferenceInvalid
	case errors.Is(err, images.ErrInspectFailed):
		return connect.CodeUnavailable, apierror.ReasonImageBackendUnavailable
	case errors.Is(err, projects.ErrUnavailable):
		// The apply path raises ErrUnavailable for dependency failures that
		// have no more specific sentinel.
		return connect.CodeUnavailable, apierror.ReasonInternal
	case errors.Is(err, projects.ErrInvalidRequest):
		return connect.CodeInvalidArgument, apierror.ReasonInvalidRequest
	case errors.Is(err, projects.ErrUnimplemented):
		return connect.CodeUnimplemented, apierror.ReasonUnsupported
	default:
		return classifyDomainError(err, ResourceProject)
	}
}
