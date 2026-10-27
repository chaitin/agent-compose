package errdetail

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestNewAndFromRoundTrip(t *testing.T) {
	cause := errors.New("image absent")
	err := New(
		connect.CodeNotFound,
		agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_NOT_FOUND,
		agentcomposev2.OperationEffect_OPERATION_EFFECT_NOT_APPLIED,
		cause,
	)
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code = %s, want not_found", connect.CodeOf(err))
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause must stay reachable")
	}
	detail, ok := From(err)
	if !ok {
		t.Fatal("From did not find the detail")
	}
	if detail.Reason != agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_NOT_FOUND {
		t.Fatalf("reason = %s", detail.Reason)
	}
	if detail.Effect != agentcomposev2.OperationEffect_OPERATION_EFFECT_NOT_APPLIED {
		t.Fatalf("effect = %s", detail.Effect)
	}

	// A caller wrapping the error must still see it.
	wrapped, ok := From(fmt.Errorf("apply project: %w", err))
	if !ok || wrapped.Reason != detail.Reason || wrapped.Effect != detail.Effect {
		t.Fatalf("wrapped detail = %#v, %v", wrapped, ok)
	}
}

func TestFromWithoutDetail(t *testing.T) {
	if _, ok := From(nil); ok {
		t.Fatal("nil must not carry a detail")
	}
	if _, ok := From(errors.New("plain")); ok {
		t.Fatal("a plain error must not carry a detail")
	}
	plainConnect := connect.NewError(connect.CodeInternal, errors.New("internal"))
	if _, ok := From(plainConnect); ok {
		t.Fatal("a Connect error without a detail must report ok=false")
	}
}

func TestAttachDoesNotFailOnNil(t *testing.T) {
	Attach(nil, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_INTERNAL, agentcomposev2.OperationEffect_OPERATION_EFFECT_UNKNOWN)
}
