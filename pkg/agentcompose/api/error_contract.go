package api

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

// rpcKind is the operation direction the external error contract needs: a read
// RPC reports no effect, a write RPC must always report one.
type rpcKind uint8

const (
	rpcRead rpcKind = iota
	rpcWrite
)

// ErrorContractHandlerOptions returns the options every agentcompose v2
// service must register with. It attaches the structured failure contract at
// the transport boundary so handlers can return ordinary errors instead of
// choosing a mapper for each call site.
//
// The interceptor is intentionally backward compatible: handlers that already
// return an OperationErrorDetail keep it, and raw Connect errors keep their
// code. That lets the remaining handlers migrate incrementally.
func ErrorContractHandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(errorContractInterceptor{})}
}

// errorContractInterceptor translates the error returned by a handler into the
// single wire representation defined by errors.proto. It is the only place that
// decides the read/write effect default and attaches OperationErrorDetail.
type errorContractInterceptor struct{}

func (errorContractInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		response, err := next(ctx, req)
		return response, translateError(req.Spec().Procedure, err)
	}
}

func (errorContractInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		// A streaming status cannot change after the first frame, but the
		// detail still travels in the trailing error.
		return translateError(conn.Spec().Procedure, next(ctx, conn))
	}
}

// WrapStreamingClient is a no-op: this interceptor translates server-side
// handler errors only.
func (errorContractInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// translateError is the single translation point. Precedence:
//
//  1. A typed apierror.Failure states why the operation failed.
//  2. An error that already carries a detail keeps it; only the effect is
//     normalized when the direction changed.
//  3. A raw Connect error keeps its code and gets a reason derived from it.
//  4. Any other domain error is classified by the shared sentinel table.
//
// Writes never claim NOT_APPLIED unless apierror.EffectFor finds a marker or an
// explicit Failure effect; anything else is UNKNOWN.
func translateError(procedure string, err error) error {
	if err == nil {
		return nil
	}
	kind := rpcKindFor(procedure)
	effect := effectFor(kind, err)

	var failure *apierror.Failure
	if errors.As(err, &failure) {
		code := connect.CodeInternal
		reason := failure.Reason
		if reason != apierror.ReasonUnspecified {
			code = codeForReason(reason)
		} else if connectErr, ok := errors.AsType[*connect.Error](err); ok {
			// The producer gave no reason but the call site chose a code.
			code = connectErr.Code()
			reason = reasonForConnectCode(code, ResourceGeneric)
		} else {
			reason = apierror.ReasonInternal
		}
		return NewOperationError(code, reason, effect, err)
	}

	if detail, ok := errdetail.From(err); ok {
		stated := EffectFromProto(detail.Effect)
		if stated == effect {
			return err
		}
		code := connect.CodeInternal
		if connectErr, ok := errors.AsType[*connect.Error](err); ok {
			code = connectErr.Code()
		}
		return NewOperationError(code, ReasonFromProto(detail.Reason), effect, err)
	}

	if connectErr, ok := errors.AsType[*connect.Error](err); ok {
		reason := reasonForConnectCode(connectErr.Code(), ResourceGeneric)
		return NewOperationError(connectErr.Code(), reason, effect, err)
	}

	code, reason := classifyDomainError(err, ResourceGeneric)
	return NewOperationError(code, reason, effect, err)
}

// effectFor returns the only valid effect for an RPC direction. A read has no
// effect; a write is NOT_APPLIED only when the error chain says so and UNKNOWN
// otherwise.
func effectFor(kind rpcKind, err error) apierror.Effect {
	if kind == rpcRead {
		return apierror.EffectUnspecified
	}
	return apierror.EffectFor(err)
}

// rpcKindFor reports the direction of a registered procedure. An unknown
// procedure is treated as a write: reporting UNKNOWN is the fail-closed
// choice. TestErrorContractCoversEveryRPC makes an unknown procedure a CI
// failure before it can reach production.
func rpcKindFor(procedure string) rpcKind {
	if kind, ok := rpcKinds[procedure]; ok {
		return kind
	}
	return rpcWrite
}

// reasonCodes is the single reason -> Connect code table. It is a map rather
// than a switch so TestReasonCodesCoverEveryReason can enumerate the wire enum
// and fail when a new OperationErrorReason is added without a mapping.
//
// The inverse (reasonForConnectCode) is not a true inverse:
// CodeAlreadyExists and CodeFailedPrecondition both collapse to
// ReasonPreconditionFailed, and a generic CodeUnavailable maps to
// ReasonInternal. TestReasonCodeRoundTrip pins those losses explicitly.
var reasonCodes = map[apierror.Reason]connect.Code{
	apierror.ReasonProjectNotFound:          connect.CodeNotFound,
	apierror.ReasonSchedulerNotFound:        connect.CodeNotFound,
	apierror.ReasonSchedulerRunNotFound:     connect.CodeNotFound,
	apierror.ReasonRunNotFound:              connect.CodeNotFound,
	apierror.ReasonSandboxNotFound:          connect.CodeNotFound,
	apierror.ReasonImageNotFound:            connect.CodeNotFound,
	apierror.ReasonVolumeNotFound:           connect.CodeNotFound,
	apierror.ReasonResourceNotFound:         connect.CodeNotFound,
	apierror.ReasonProjectSpecInvalid:       connect.CodeInvalidArgument,
	apierror.ReasonAgentDefinitionInvalid:   connect.CodeInvalidArgument,
	apierror.ReasonSchedulerInvalid:         connect.CodeInvalidArgument,
	apierror.ReasonSchedulerScriptInvalid:   connect.CodeInvalidArgument,
	apierror.ReasonNetworkPolicyRejected:    connect.CodeInvalidArgument,
	apierror.ReasonInvalidRequest:           connect.CodeInvalidArgument,
	apierror.ReasonImageReferenceInvalid:    connect.CodeInvalidArgument,
	apierror.ReasonRevisionConflict:         connect.CodeFailedPrecondition,
	apierror.ReasonPreconditionFailed:       connect.CodeFailedPrecondition,
	apierror.ReasonImageInUse:               connect.CodeFailedPrecondition,
	apierror.ReasonOperationBusy:            connect.CodeAborted,
	apierror.ReasonUnsupported:              connect.CodeUnimplemented,
	apierror.ReasonOperationCanceled:        connect.CodeCanceled,
	apierror.ReasonOperationTimeout:         connect.CodeDeadlineExceeded,
	apierror.ReasonSchedulerReconcileFailed: connect.CodeInternal,
	apierror.ReasonImagePullFailed:          connect.CodeInternal,
	apierror.ReasonImageBuildFailed:         connect.CodeInternal,
	apierror.ReasonSandboxCreationFailed:    connect.CodeInternal,
	apierror.ReasonSandboxStopFailed:        connect.CodeInternal,
	apierror.ReasonInternal:                 connect.CodeInternal,
	apierror.ReasonImageBackendUnavailable:  connect.CodeUnavailable,
	apierror.ReasonVolumeUnavailable:        connect.CodeUnavailable,
	apierror.ReasonSandboxRuntimeUnavail:    connect.CodeUnavailable,
	apierror.ReasonStorageUnavailable:       connect.CodeUnavailable,
}

// codeForReason maps the stable failure category to a Connect code. Reasons
// are the wire contract; the code is the conventional transport rendering.
func codeForReason(reason apierror.Reason) connect.Code {
	if code, ok := reasonCodes[reason]; ok {
		return code
	}
	return connect.CodeInternal
}
