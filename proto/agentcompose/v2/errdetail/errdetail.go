// Package errdetail builds and reads the structured error detail the daemon
// attaches to external RPC failures.
//
// The wire type lives in the shared proto module so clients can depend on it
// without inheriting the daemon's runtime dependencies. Servers attach one
// OperationErrorDetail to a Connect error; clients read it with From and must
// fall back to fail-closed behaviour when it is absent, for example because an
// older daemon answered or the failure never reached the daemon.
package errdetail

import (
	"errors"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// Detail is the decoded OperationErrorDetail.
type Detail struct {
	Reason agentcomposev2.OperationErrorReason
	Effect agentcomposev2.OperationEffect
}

// New returns a Connect error carrying reason and effect. A detail that cannot
// be marshalled is dropped rather than failing the RPC: the caller still
// receives the original code and cause, and clients treat the missing detail
// as UNKNOWN.
func New(code connect.Code, reason agentcomposev2.OperationErrorReason, effect agentcomposev2.OperationEffect, cause error) *connect.Error {
	err := connect.NewError(code, cause)
	Attach(err, reason, effect)
	return err
}

// Attach adds the detail to err. Passing an already classified error is
// allowed: the detail is appended, so a caller that wraps an existing error
// must construct a fresh *connect.Error instead of mutating a shared one.
func Attach(err *connect.Error, reason agentcomposev2.OperationErrorReason, effect agentcomposev2.OperationEffect) {
	if err == nil {
		return
	}
	detail, detailErr := connect.NewErrorDetail(&agentcomposev2.OperationErrorDetail{
		Reason: reason,
		Effect: effect,
	})
	if detailErr != nil {
		return
	}
	err.AddDetail(detail)
}

// From reads the daemon's OperationErrorDetail from err. ok is false when the
// error is not a Connect error or carries no detail; callers must then apply
// their fail-closed rule, which for a write RPC means UNKNOWN.
func From(err error) (Detail, bool) {
	if err == nil {
		return Detail{}, false
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return Detail{}, false
	}
	for _, candidate := range connectErr.Details() {
		value, valueErr := candidate.Value()
		if valueErr != nil {
			continue
		}
		detail, ok := value.(*agentcomposev2.OperationErrorDetail)
		if !ok {
			continue
		}
		return Detail{Reason: detail.GetReason(), Effect: detail.GetEffect()}, true
	}
	return Detail{}, false
}
