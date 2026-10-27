package api

import (
	"context"
	"database/sql"
	"errors"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

// ResourceKind names the resource a not-found answer refers to. It lets the
// shared domain mapper attach a specific reason instead of a generic one while
// leaving the Connect code untouched.
type ResourceKind int

const (
	ResourceGeneric ResourceKind = iota
	ResourceProject
	ResourceScheduler
	ResourceSchedulerRun
	ResourceRun
	ResourceSandbox
	ResourceImage
)

// ConnectErrorForDomain maps a domain error for a read RPC: it carries a
// reason but no effect.
func ConnectErrorForDomain(err error) error {
	return connectErrorForDomain(err, ResourceGeneric, apierror.EffectUnspecified, false)
}

// ConnectErrorForDomainKind is ConnectErrorForDomain with an attributed
// resource kind.
func ConnectErrorForDomainKind(err error, kind ResourceKind) error {
	return connectErrorForDomain(err, kind, apierror.EffectUnspecified, false)
}

// ConnectErrorForDomainWrite maps a domain error for a write RPC: the effect is
// derived from the not-applied marker, so an error that was not explicitly
// marked NOT_APPLIED reports UNKNOWN. An inner boundary may already have mapped
// the error for a read; the map is upgraded because an UNSPECIFIED effect is
// not a valid answer on a write RPC.
func ConnectErrorForDomainWrite(err error, kind ResourceKind) error {
	return connectErrorForDomain(err, kind, apierror.EffectFor(err), true)
}

func connectErrorForDomain(err error, kind ResourceKind, effect apierror.Effect, upgradeUnspecified bool) error {
	if err == nil {
		return nil
	}
	if detail, ok := errdetail.From(err); ok {
		if !upgradeUnspecified {
			// The inner boundary had more information than a code-to-reason
			// mapping does.
			return err
		}
		return preserveOrUpgradeDetail(err, detail, effect)
	}
	// Handlers and adapters often return an already mapped *connect.Error.
	// Attaching the detail in place preserves its code and message.
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		attachOperationDetail(connectErr, reasonForConnectCode(connectErr.Code(), kind), effect)
		return connectErr
	}
	code, reason := classifyDomainError(err, kind)
	return NewOperationError(code, reason, effect, err)
}

// ErrorForCode builds a read-RPC error with a reason derived from the code and
// the resource kind. Use it where a handler already knows the code.
func ErrorForCode(code connect.Code, kind ResourceKind, cause error) error {
	return NewOperationError(code, reasonForConnectCode(code, kind), apierror.EffectUnspecified, cause)
}

// WriteErrorForCode builds a write-RPC error with a reason derived from the
// code. It reports UNKNOWN: only an explicit NOT_APPLIED marker may say less.
func WriteErrorForCode(code connect.Code, kind ResourceKind, cause error) error {
	return NewOperationError(code, reasonForConnectCode(code, kind), apierror.EffectUnknown, cause)
}

// preserveOrUpgradeDetail keeps a detail an inner boundary attached, but a
// write boundary must not put UNSPECIFIED on the wire: the effect is upgraded
// while the inner reason (which may be more precise) is preserved. A
// NOT_APPLIED marker on the outer error wins over an inner boundary's
// UNKNOWN, because the marker is the only way to claim NOT_APPLIED and must
// not be silently downgraded.
func preserveOrUpgradeDetail(err error, detail errdetail.Detail, effect apierror.Effect) error {
	if EffectFromProto(detail.Effect) != apierror.EffectUnspecified && effect != apierror.EffectNotApplied {
		return err
	}
	reason := ReasonFromProto(detail.Reason)
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return NewOperationError(connectErr.Code(), reason, effect, connectErr)
	}
	return NewOperationError(connect.CodeInternal, reason, effect, err)
}

func reasonForConnectCode(code connect.Code, kind ResourceKind) apierror.Reason {
	switch code {
	case connect.CodeNotFound:
		return notFoundReason(kind)
	case connect.CodeInvalidArgument:
		return apierror.ReasonInvalidRequest
	case connect.CodeFailedPrecondition, connect.CodeAlreadyExists:
		return apierror.ReasonPreconditionFailed
	case connect.CodeUnimplemented:
		return apierror.ReasonUnsupported
	case connect.CodeCanceled:
		return apierror.ReasonOperationCanceled
	case connect.CodeDeadlineExceeded:
		return apierror.ReasonOperationTimeout
	case connect.CodeAborted:
		return apierror.ReasonOperationBusy
	case connect.CodeUnavailable:
		if kind == ResourceSandbox {
			return apierror.ReasonSandboxRuntimeUnavail
		}
		if kind == ResourceImage {
			return apierror.ReasonImageBackendUnavailable
		}
		return apierror.ReasonInternal
	default:
		return apierror.ReasonInternal
	}
}

func classifyDomainError(err error, kind ResourceKind) (connect.Code, apierror.Reason) {
	switch {
	case errors.Is(err, context.Canceled):
		return connect.CodeCanceled, apierror.ReasonOperationCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return connect.CodeDeadlineExceeded, apierror.ReasonOperationTimeout
	case errors.Is(err, domain.ErrUnsupported):
		return connect.CodeUnimplemented, apierror.ReasonUnsupported
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, domain.ErrNotFound):
		return connect.CodeNotFound, notFoundReason(kind)
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, domain.ErrRequired), errors.Is(err, domain.ErrAmbiguous):
		return connect.CodeInvalidArgument, apierror.ReasonInvalidRequest
	case errors.Is(err, domain.ErrFailedPrecondition), errors.Is(err, domain.ErrConflict), errors.Is(err, domain.ErrReferenced):
		return connect.CodeFailedPrecondition, apierror.ReasonPreconditionFailed
	case errors.Is(err, domain.ErrAlreadyExists):
		return connect.CodeAlreadyExists, apierror.ReasonPreconditionFailed
	default:
		return connect.CodeInternal, apierror.ReasonInternal
	}
}

func notFoundReason(kind ResourceKind) apierror.Reason {
	switch kind {
	case ResourceProject:
		return apierror.ReasonProjectNotFound
	case ResourceScheduler:
		return apierror.ReasonSchedulerNotFound
	case ResourceSchedulerRun:
		return apierror.ReasonSchedulerRunNotFound
	case ResourceRun:
		return apierror.ReasonRunNotFound
	case ResourceSandbox:
		return apierror.ReasonSandboxNotFound
	case ResourceImage:
		return apierror.ReasonImageNotFound
	default:
		return apierror.ReasonResourceNotFound
	}
}
