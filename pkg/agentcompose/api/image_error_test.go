package api

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/imagecache"
	"github.com/chaitin/agent-compose/pkg/images"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
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

func TestConnectErrorForImageBackendWritePullFailureIsUnknown(t *testing.T) {
	pullFailed := images.OpError{Op: "pull image", ImageRef: "guest:latest", Err: imagecache.NewError(imagecache.ErrorKindUnavailable, "pull", "guest:latest", errors.New("registry down"))}
	mapped := ConnectErrorForImageBackendWrite("pull image", "guest:latest", apierror.ReasonImagePullFailed, pullFailed)
	if got := connect.CodeOf(mapped); got != connect.CodeUnavailable {
		t.Fatalf("code = %s, want unavailable", got)
	}
	detail, ok := errdetail.From(mapped)
	if !ok {
		t.Fatal("mapped error carries no detail")
	}
	if ReasonFromProto(detail.Reason) != apierror.ReasonImagePullFailed {
		t.Fatalf("reason = %d, want image_pull_failed", ReasonFromProto(detail.Reason))
	}
	if detail.Effect != EffectToProto(apierror.EffectUnknown) {
		t.Fatalf("effect = %s, want unknown (may have partially pulled)", detail.Effect)
	}
}

func TestImageHandlersReportReasonForMissingRef(t *testing.T) {
	handler := NewImageHandler(fakeImageSelector{})
	tests := []struct {
		name   string
		call   func() error
		effect apierror.Effect
	}{
		{
			name: "inspect reads unspecified effect",
			call: func() error {
				_, err := handler.InspectImage(context.Background(), connect.NewRequest(&agentcomposev2.InspectImageRequest{}))
				return err
			},
			effect: apierror.EffectUnspecified,
		},
		{
			name: "pull reports not applied",
			call: func() error {
				_, err := handler.PullImage(context.Background(), connect.NewRequest(&agentcomposev2.PullImageRequest{}))
				return err
			},
			effect: apierror.EffectNotApplied,
		},
		{
			name: "remove reports not applied",
			call: func() error {
				_, err := handler.RemoveImage(context.Background(), connect.NewRequest(&agentcomposev2.RemoveImageRequest{}))
				return err
			},
			effect: apierror.EffectNotApplied,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid argument (err=%v)", connect.CodeOf(err), err)
			}
			detail, ok := errdetail.From(err)
			if !ok {
				t.Fatal("error carries no detail")
			}
			if ReasonFromProto(detail.Reason) != apierror.ReasonInvalidRequest {
				t.Fatalf("reason = %d, want invalid request", ReasonFromProto(detail.Reason))
			}
			if EffectFromProto(detail.Effect) != tc.effect {
				t.Fatalf("effect = %v, want %v", EffectFromProto(detail.Effect), tc.effect)
			}
		})
	}
}

func TestImageHandlersReportReasonForSelectorFailure(t *testing.T) {
	tests := []struct {
		name   string
		call   func(*ImageHandler) error
		effect apierror.Effect
	}{
		{
			name: "list reads unspecified effect",
			call: func(h *ImageHandler) error {
				_, err := h.ListImages(context.Background(), connect.NewRequest(&agentcomposev2.ListImagesRequest{}))
				return err
			},
			effect: apierror.EffectUnspecified,
		},
		{
			name: "inspect reads unspecified effect",
			call: func(h *ImageHandler) error {
				_, err := h.InspectImage(context.Background(), connect.NewRequest(&agentcomposev2.InspectImageRequest{ImageRef: "guest:latest"}))
				return err
			},
			effect: apierror.EffectUnspecified,
		},
		{
			name: "pull reports not applied",
			call: func(h *ImageHandler) error {
				_, err := h.PullImage(context.Background(), connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "guest:latest"}))
				return err
			},
			effect: apierror.EffectNotApplied,
		},
		{
			name: "remove reports not applied",
			call: func(h *ImageHandler) error {
				_, err := h.RemoveImage(context.Background(), connect.NewRequest(&agentcomposev2.RemoveImageRequest{ImageRef: "guest:latest"}))
				return err
			},
			effect: apierror.EffectNotApplied,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewImageHandler(failingImageSelector{err: connect.NewError(connect.CodeInvalidArgument, errors.New("unsupported image store"))})
			err := tc.call(handler)
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid argument (err=%v)", connect.CodeOf(err), err)
			}
			detail, ok := errdetail.From(err)
			if !ok {
				t.Fatal("error carries no detail")
			}
			if ReasonFromProto(detail.Reason) != apierror.ReasonInvalidRequest {
				t.Fatalf("reason = %d, want invalid request", ReasonFromProto(detail.Reason))
			}
			if EffectFromProto(detail.Effect) != tc.effect {
				t.Fatalf("effect = %v, want %v", EffectFromProto(detail.Effect), tc.effect)
			}
		})
	}
}

type failingImageSelector struct{ err error }

func (s failingImageSelector) ImageBackendForStore(agentcomposev2.ImageStoreKind) (images.Backend, error) {
	return nil, s.err
}
