package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/imagecache"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

func TestConnectErrorForImageBackendWriteNotApplied(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason apierror.Reason
	}{
		{
			name:   "image not found",
			err:    images.OpError{Op: "remove image", ImageRef: "missing:latest", Err: imagecache.NewError(imagecache.ErrorKindNotFound, "remove", "missing:latest", errors.New("missing"))},
			reason: apierror.ReasonImageNotFound,
		},
		{
			name:   "reference invalid",
			err:    images.OpError{Op: "remove image", ImageRef: "bad ref", Err: imagecache.NewError(imagecache.ErrorKindInvalidReference, "parse", "bad ref", errors.New("invalid"))},
			reason: apierror.ReasonImageReferenceInvalid,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mapped := ConnectErrorForImageBackendWrite("remove image", "missing:latest", apierror.ReasonImageBackendUnavailable, tc.err)
			detail, ok := errdetail.From(mapped)
			if !ok {
				t.Fatal("mapped error carries no detail")
			}
			if ReasonFromProto(detail.Reason) != tc.reason {
				t.Fatalf("reason = %d, want %d", ReasonFromProto(detail.Reason), tc.reason)
			}
			if detail.Effect != EffectToProto(apierror.EffectNotApplied) {
				t.Fatalf("effect = %s, want not_applied", detail.Effect)
			}
		})
	}
}

func TestConnectErrorForImageBackendWriteContextCancellation(t *testing.T) {
	cancelled := images.OpError{Op: "pull image", ImageRef: "guest:latest", Err: context.Canceled}
	mapped := ConnectErrorForImageBackendWrite("pull image", "guest:latest", apierror.ReasonImagePullFailed, cancelled)
	if got := connect.CodeOf(mapped); got != connect.CodeCanceled {
		t.Fatalf("code = %s, want canceled", got)
	}
	detail, ok := errdetail.From(mapped)
	if !ok || ReasonFromProto(detail.Reason) != apierror.ReasonOperationCanceled {
		t.Fatalf("detail = %#v, %v", detail, ok)
	}
}
