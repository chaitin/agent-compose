package api

import (
	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

// NewOperationError converts the transport-neutral vocabulary into the wire
// detail. This is the only place server code turns an apierror.Reason or
// apierror.Effect into a protobuf enum, which keeps domain packages free of
// transport types (AGENTS.md).
func NewOperationError(code connect.Code, reason apierror.Reason, effect apierror.Effect, cause error) *connect.Error {
	return errdetail.New(code, ReasonToProto(reason), EffectToProto(effect), cause)
}

func attachOperationDetail(err *connect.Error, reason apierror.Reason, effect apierror.Effect) {
	errdetail.Attach(err, ReasonToProto(reason), EffectToProto(effect))
}

// ReasonFromProto is the reverse conversion used when a proto issue is read
// back into the application layer.
func ReasonFromProto(reason agentcomposev2.OperationErrorReason) apierror.Reason {
	return apierror.Reason(reason)
}

// EffectFromProto is the reverse conversion for the effect vocabulary.
func EffectFromProto(effect agentcomposev2.OperationEffect) apierror.Effect {
	return apierror.Effect(effect)
}

// ReasonToProto converts the transport-neutral reason at the wire boundary.
func ReasonToProto(reason apierror.Reason) agentcomposev2.OperationErrorReason {
	return agentcomposev2.OperationErrorReason(reason)
}

// EffectToProto converts the transport-neutral effect at the wire boundary.
func EffectToProto(effect apierror.Effect) agentcomposev2.OperationEffect {
	return agentcomposev2.OperationEffect(effect)
}
