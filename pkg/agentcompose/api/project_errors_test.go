package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/apierror"
	"github.com/chaitin/agent-compose/pkg/images"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

func TestConnectErrorForProjectClassification(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   connect.Code
		reason apierror.Reason
	}{
		{"revision conflict", projects.ErrRevisionConflict, connect.CodeAborted, apierror.ReasonRevisionConflict},
		{"volume not found", projects.ErrVolumeNotFound, connect.CodeNotFound, apierror.ReasonVolumeNotFound},
		{"volume unavailable", projects.ErrVolumeUnavailable, connect.CodeUnavailable, apierror.ReasonVolumeUnavailable},
		{"image not found", fmt.Errorf("wrapped: %w", images.ErrImageNotFound), connect.CodeUnavailable, apierror.ReasonImageNotFound},
		{"image pull failed", images.ErrPullFailed, connect.CodeUnavailable, apierror.ReasonImagePullFailed},
		{"image reference invalid", images.ErrInvalidReference, connect.CodeInvalidArgument, apierror.ReasonImageReferenceInvalid},
		{"image backend unavailable", images.ErrInspectFailed, connect.CodeUnavailable, apierror.ReasonImageBackendUnavailable},
		{"dependency unavailable without a specific cause", projects.ErrUnavailable, connect.CodeUnavailable, apierror.ReasonInternal},
		{"invalid request", projects.ErrInvalidRequest, connect.CodeInvalidArgument, apierror.ReasonInvalidRequest},
		{"unimplemented", projects.ErrUnimplemented, connect.CodeUnimplemented, apierror.ReasonUnsupported},
		{"not found", fmt.Errorf("wrapped: %w", domain.ErrNotFound), connect.CodeNotFound, apierror.ReasonProjectNotFound},
		{"precondition", domain.ErrFailedPrecondition, connect.CodeFailedPrecondition, apierror.ReasonPreconditionFailed},
		{"unclassified", errors.New("boom"), connect.CodeInternal, apierror.ReasonInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mapped := ConnectErrorForProject(tc.err)
			if got := connect.CodeOf(mapped); got != tc.code {
				t.Fatalf("code = %s, want %s", got, tc.code)
			}
			detail, ok := errdetail.From(mapped)
			if !ok {
				t.Fatal("mapped error carries no detail")
			}
			if got := ReasonFromProto(detail.Reason); got != tc.reason {
				t.Fatalf("reason = %d, want %d", got, tc.reason)
			}
			if detail.Effect != EffectToProto(apierror.EffectUnknown) {
				t.Fatalf("effect = %s, want unknown (no marker)", detail.Effect)
			}
		})
	}
}

func TestConnectErrorForProjectUpgradesReadDetail(t *testing.T) {
	// Project writes must never leak a read-style UNSPECIFIED effect, even when
	// an inner boundary already attached the detail.
	readMapped := ConnectErrorForDomainKind(domain.ErrNotFound, ResourceProject)
	readDetail, ok := errdetail.From(readMapped)
	if !ok || readDetail.Effect != EffectToProto(apierror.EffectUnspecified) {
		t.Fatalf("read detail = %#v, %v", readDetail, ok)
	}
	writeMapped := ConnectErrorForProject(readMapped)
	detail, ok := errdetail.From(writeMapped)
	if !ok {
		t.Fatal("project mapping dropped the detail")
	}
	if detail.Effect != EffectToProto(apierror.EffectUnknown) {
		t.Fatalf("effect = %s, want unknown", detail.Effect)
	}
	if ReasonFromProto(detail.Reason) != apierror.ReasonProjectNotFound {
		t.Fatalf("reason = %d, want project_not_found", ReasonFromProto(detail.Reason))
	}
	if connect.CodeOf(writeMapped) != connect.CodeNotFound {
		t.Fatalf("code = %s, want not_found", connect.CodeOf(writeMapped))
	}
}

func TestConnectErrorForProjectHonoursNotApplied(t *testing.T) {
	mapped := ConnectErrorForProject(apierror.NotApplied(images.ErrImageNotFound))
	detail, ok := errdetail.From(mapped)
	if !ok {
		t.Fatal("mapped error carries no detail")
	}
	if detail.Effect != EffectToProto(apierror.EffectNotApplied) {
		t.Fatalf("effect = %s, want not_applied", detail.Effect)
	}
}

func TestWriteMappingUpgradesReadDetail(t *testing.T) {
	// An inner read boundary attached UNSPECIFIED; a write boundary must not
	// leave that on the wire.
	readMapped := ConnectErrorForDomainKind(domain.ErrNotFound, ResourceSandbox)
	detail, ok := errdetail.From(readMapped)
	if !ok || detail.Effect != EffectToProto(apierror.EffectUnspecified) {
		t.Fatalf("read detail = %#v, %v", detail, ok)
	}
	writeMapped := ConnectErrorForDomainWrite(readMapped, ResourceSandbox)
	detail, ok = errdetail.From(writeMapped)
	if !ok {
		t.Fatal("write mapping dropped the detail")
	}
	if detail.Effect != EffectToProto(apierror.EffectUnknown) {
		t.Fatalf("upgraded effect = %s, want unknown", detail.Effect)
	}
	if connect.CodeOf(writeMapped) != connect.CodeNotFound {
		t.Fatalf("upgraded code = %s, want not_found", connect.CodeOf(writeMapped))
	}

	// A marker on the inner error still wins.
	markedRead := ConnectErrorForDomainKind(apierror.NotApplied(domain.ErrNotFound), ResourceSandbox)
	writeMarked := ConnectErrorForDomainWrite(markedRead, ResourceSandbox)
	detail, ok = errdetail.From(writeMarked)
	if !ok || detail.Effect != EffectToProto(apierror.EffectNotApplied) {
		t.Fatalf("marked detail = %#v, %v", detail, ok)
	}
}

func TestCodeHelpersPickTheRightEffect(t *testing.T) {
	read := ErrorForCode(connect.CodeNotFound, ResourceRun, errors.New("missing"))
	if detail, _ := errdetail.From(read); detail.Effect != EffectToProto(apierror.EffectUnspecified) || ReasonFromProto(detail.Reason) != apierror.ReasonRunNotFound {
		t.Fatalf("read helper detail = %#v", detail)
	}
	write := WriteErrorForCode(connect.CodeUnavailable, ResourceSandbox, errors.New("down"))
	if detail, _ := errdetail.From(write); detail.Effect != EffectToProto(apierror.EffectUnknown) || ReasonFromProto(detail.Reason) != apierror.ReasonSandboxRuntimeUnavail {
		t.Fatalf("write helper detail = %#v", detail)
	}
}

func TestConnectErrorForProjectContextCancellation(t *testing.T) {
	// A cancellation that got wrapped into an image sentinel by the ensure path
	// must still surface as CANCELED, not as an image backend failure.
	cancelled := fmt.Errorf("%w: %w", images.ErrInspectFailed, context.Canceled)
	mapped := ConnectErrorForProject(cancelled)
	if got := connect.CodeOf(mapped); got != connect.CodeCanceled {
		t.Fatalf("code = %s, want canceled", got)
	}
	detail, ok := errdetail.From(mapped)
	if !ok || ReasonFromProto(detail.Reason) != apierror.ReasonOperationCanceled {
		t.Fatalf("detail = %#v, %v", detail, ok)
	}
}

func TestMarkerOverridesInnerUnknownEffect(t *testing.T) {
	// An inner boundary already reported UNKNOWN; an outer NOT_APPLIED marker
	// must win because the marker is the only way to claim NOT_APPLIED.
	inner := NewOperationError(connect.CodeInternal, apierror.ReasonInternal, apierror.EffectUnknown, errors.New("boom"))
	marked := apierror.NotApplied(inner)
	if !apierror.IsNotApplied(marked) {
		t.Fatal("marker was not preserved")
	}
	mapped := ConnectErrorForProject(marked)
	detail, ok := errdetail.From(mapped)
	if !ok {
		t.Fatal("mapped error carries no detail")
	}
	if detail.Effect != EffectToProto(apierror.EffectNotApplied) {
		t.Fatalf("effect = %s, want not_applied (marker must override inner unknown)", detail.Effect)
	}
}

func TestConnectErrorForDomainKinds(t *testing.T) {
	read := ConnectErrorForDomainKind(domain.ErrNotFound, ResourceRun)
	detail, ok := errdetail.From(read)
	if !ok || ReasonFromProto(detail.Reason) != apierror.ReasonRunNotFound || detail.Effect != EffectToProto(apierror.EffectUnspecified) {
		t.Fatalf("read detail = %#v, %v", detail, ok)
	}

	write := ConnectErrorForDomainWrite(apierror.NotApplied(domain.ErrNotFound), ResourceSandbox)
	detail, ok = errdetail.From(write)
	if !ok || ReasonFromProto(detail.Reason) != apierror.ReasonSandboxNotFound || detail.Effect != EffectToProto(apierror.EffectNotApplied) {
		t.Fatalf("write detail = %#v, %v", detail, ok)
	}

	// An already mapped Connect error keeps its code and gains a detail.
	alreadyMapped := connect.NewError(connect.CodeUnavailable, errors.New("runtime down"))
	write = ConnectErrorForDomainWrite(alreadyMapped, ResourceSandbox)
	if connect.CodeOf(write) != connect.CodeUnavailable {
		t.Fatalf("code = %s, want unavailable", connect.CodeOf(write))
	}
	detail, ok = errdetail.From(write)
	if !ok || ReasonFromProto(detail.Reason) != apierror.ReasonSandboxRuntimeUnavail || detail.Effect != EffectToProto(apierror.EffectUnknown) {
		t.Fatalf("pre-mapped detail = %#v, %v", detail, ok)
	}
}

func TestConnectErrorForProjectPreservesPremappedCode(t *testing.T) {
	// A controller chain may return an already mapped *connect.Error without a
	// detail; the write mapper must keep its code instead of reclassifying the
	// wrapped cause as Internal.
	premapped := connect.NewError(connect.CodeFailedPrecondition, errors.New("conflict"))
	mapped := ConnectErrorForProject(premapped)
	if got := connect.CodeOf(mapped); got != connect.CodeFailedPrecondition {
		t.Fatalf("code = %s, want failed_precondition", got)
	}
	detail, ok := errdetail.From(mapped)
	if !ok {
		t.Fatal("mapped error carries no detail")
	}
	if ReasonFromProto(detail.Reason) != apierror.ReasonPreconditionFailed {
		t.Fatalf("reason = %d, want precondition_failed", ReasonFromProto(detail.Reason))
	}
	if detail.Effect != EffectToProto(apierror.EffectUnknown) {
		t.Fatalf("effect = %s, want unknown (no marker)", detail.Effect)
	}
}

func TestConnectErrorForProjectDoesNotTrustUnmarkedNotApplied(t *testing.T) {
	// An inner boundary attached NOT_APPLIED without the marker; the write
	// mapper must upgrade it to UNKNOWN because the marker is the only way to
	// claim NOT_APPLIED.
	inner := NewOperationError(connect.CodeNotFound, apierror.ReasonProjectNotFound, apierror.EffectNotApplied, errors.New("boom"))
	if apierror.IsNotApplied(inner) {
		t.Fatal("test setup: inner error must carry no marker")
	}
	mapped := ConnectErrorForProject(inner)
	detail, ok := errdetail.From(mapped)
	if !ok {
		t.Fatal("mapped error carries no detail")
	}
	if detail.Effect != EffectToProto(apierror.EffectUnknown) {
		t.Fatalf("effect = %s, want unknown (marker absent)", detail.Effect)
	}
	if ReasonFromProto(detail.Reason) != apierror.ReasonProjectNotFound {
		t.Fatalf("reason = %d, want project_not_found", ReasonFromProto(detail.Reason))
	}
	if connect.CodeOf(mapped) != connect.CodeNotFound {
		t.Fatalf("code = %s, want not_found", connect.CodeOf(mapped))
	}
}
