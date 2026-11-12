package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	agentcomposev2connect "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

func TestTranslateError(t *testing.T) {
	cause := errors.New("boom")
	existingUnknown := NewOperationError(connect.CodeUnavailable, apierror.ReasonSandboxRuntimeUnavail, apierror.EffectUnspecified, cause)
	existingNotApplied := NewOperationError(connect.CodeInvalidArgument, apierror.ReasonProjectSpecInvalid, apierror.EffectNotApplied, cause)
	markedNotApplied := apierror.NotApplied(NewOperationError(connect.CodeInternal, apierror.ReasonInternal, apierror.EffectUnspecified, cause))

	tests := []struct {
		name       string
		procedure  string
		err        error
		wantCode   connect.Code
		wantReason apierror.Reason
		wantEffect apierror.Effect
	}{
		{
			name:       "typed failure on a read carries the reason without an effect",
			procedure:  "/agentcompose.v2.ProjectService/GetProject",
			err:        apierror.NewFailure(apierror.ReasonProjectNotFound, cause),
			wantCode:   connect.CodeNotFound,
			wantReason: apierror.ReasonProjectNotFound,
			wantEffect: apierror.EffectUnspecified,
		},
		{
			name:       "typed failure on a write defaults to unknown",
			procedure:  "/agentcompose.v2.VolumeService/CreateVolume",
			err:        apierror.NewFailure(apierror.ReasonVolumeNotFound, cause),
			wantCode:   connect.CodeNotFound,
			wantReason: apierror.ReasonVolumeNotFound,
			wantEffect: apierror.EffectUnknown,
		},
		{
			name:       "typed not applied is preserved on a write",
			procedure:  "/agentcompose.v2.ProjectService/ApplyProject",
			err:        apierror.NewNotAppliedFailure(apierror.ReasonProjectSpecInvalid, cause),
			wantCode:   connect.CodeInvalidArgument,
			wantReason: apierror.ReasonProjectSpecInvalid,
			wantEffect: apierror.EffectNotApplied,
		},
		{
			name:       "typed unknown is preserved on a write",
			procedure:  "/agentcompose.v2.ImageService/PullImage",
			err:        apierror.NewUnknownFailure(apierror.ReasonImagePullFailed, cause),
			wantCode:   connect.CodeInternal,
			wantReason: apierror.ReasonImagePullFailed,
			wantEffect: apierror.EffectUnknown,
		},
		{
			name:       "a write effect cannot leak into a read",
			procedure:  "/agentcompose.v2.ProjectService/GetProject",
			err:        apierror.NewNotAppliedFailure(apierror.ReasonProjectSpecInvalid, cause),
			wantCode:   connect.CodeInvalidArgument,
			wantReason: apierror.ReasonProjectSpecInvalid,
			wantEffect: apierror.EffectUnspecified,
		},
		{
			name:       "legacy write mapped with the read helper is upgraded to unknown",
			procedure:  "/agentcompose.v2.VolumeService/CreateVolume",
			err:        existingUnknown,
			wantCode:   connect.CodeUnavailable,
			wantReason: apierror.ReasonSandboxRuntimeUnavail,
			wantEffect: apierror.EffectUnknown,
		},
		{
			name:       "not applied without a marker is not trusted on a write",
			procedure:  "/agentcompose.v2.VolumeService/RemoveVolume",
			err:        existingNotApplied,
			wantCode:   connect.CodeInvalidArgument,
			wantReason: apierror.ReasonProjectSpecInvalid,
			wantEffect: apierror.EffectUnknown,
		},
		{
			name:       "not applied with a marker is preserved on a write",
			procedure:  "/agentcompose.v2.VolumeService/RemoveVolume",
			err:        markedNotApplied,
			wantCode:   connect.CodeInternal,
			wantReason: apierror.ReasonInternal,
			wantEffect: apierror.EffectNotApplied,
		},
		{
			name:       "a raw connect error keeps its code and gains a contract",
			procedure:  "/agentcompose.v2.VolumeService/PruneVolumes",
			err:        connect.NewError(connect.CodeInvalidArgument, cause),
			wantCode:   connect.CodeInvalidArgument,
			wantReason: apierror.ReasonInvalidRequest,
			wantEffect: apierror.EffectUnknown,
		},
		{
			name:       "a domain sentinel is classified",
			procedure:  "/agentcompose.v2.RunService/GetRun",
			err:        domain.ErrNotFound,
			wantCode:   connect.CodeNotFound,
			wantReason: apierror.ReasonResourceNotFound,
			wantEffect: apierror.EffectUnspecified,
		},
		{
			name:       "cancellation on a write reports an unknown effect",
			procedure:  "/agentcompose.v2.RunService/StartAgentRun",
			err:        context.Canceled,
			wantCode:   connect.CodeCanceled,
			wantReason: apierror.ReasonOperationCanceled,
			wantEffect: apierror.EffectUnknown,
		},
		{
			name:       "an unregistered procedure fails closed as a write",
			procedure:  "/agentcompose.v2.UnregisteredService/Mutate",
			err:        cause,
			wantCode:   connect.CodeInternal,
			wantReason: apierror.ReasonInternal,
			wantEffect: apierror.EffectUnknown,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			translated := translateError(tc.procedure, tc.err)
			detail, ok := errdetail.From(translated)
			if !ok {
				t.Fatalf("translateError(%v) = %v, no OperationErrorDetail", tc.err, translated)
			}
			if got := connect.CodeOf(translated); got != tc.wantCode {
				t.Errorf("code = %v, want %v", got, tc.wantCode)
			}
			if got := ReasonFromProto(detail.Reason); got != tc.wantReason {
				t.Errorf("reason = %d, want %d", got, tc.wantReason)
			}
			if got := EffectFromProto(detail.Effect); got != tc.wantEffect {
				t.Errorf("effect = %d, want %d", got, tc.wantEffect)
			}
		})
	}
}

func TestTranslateErrorKeepsCompleteDetail(t *testing.T) {
	cause := errors.New("boom")
	complete := NewOperationError(connect.CodeNotFound, apierror.ReasonProjectNotFound, apierror.EffectUnspecified, cause)
	if got := translateError("/agentcompose.v2.ProjectService/GetProject", complete); got != complete {
		t.Errorf("translateError() = %v, want the already complete error unchanged", got)
	}
}

func TestErrorContractInterceptorUsesProcedure(t *testing.T) {
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewVolumeServiceHandler(failingVolumeService{}, ErrorContractHandlerOptions()...)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := agentcomposev2connect.NewVolumeServiceClient(server.Client(), server.URL)
	_, err := client.CreateVolume(context.Background(), connect.NewRequest(&agentcomposev2.CreateVolumeRequest{Name: "data"}))
	if err == nil {
		t.Fatal("CreateVolume() = nil error, want a failure")
	}
	detail, ok := errdetail.From(err)
	if !ok {
		t.Fatalf("CreateVolume() = %v, no OperationErrorDetail", err)
	}
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want %v", got, connect.CodeNotFound)
	}
	if got := EffectFromProto(detail.Effect); got != apierror.EffectUnknown {
		t.Errorf("effect = %d, want %d", got, apierror.EffectUnknown)
	}
}

type failingVolumeService struct {
	agentcomposev2connect.UnimplementedVolumeServiceHandler
}

func (failingVolumeService) CreateVolume(context.Context, *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error) {
	return nil, apierror.NewFailure(apierror.ReasonVolumeNotFound, errors.New("boom"))
}

func TestErrorContractCoversEveryRPC(t *testing.T) {
	services := agentcomposev2.File_agentcompose_v2_agentcompose_proto.Services()
	seen := make(map[string]bool)
	for i := 0; i < services.Len(); i++ {
		service := services.Get(i)
		methods := service.Methods()
		for j := 0; j < methods.Len(); j++ {
			procedure := "/" + string(service.FullName()) + "/" + string(methods.Get(j).Name())
			seen[procedure] = true
			if _, ok := rpcKinds[procedure]; !ok {
				t.Errorf("RPC %s has no read/write classification", procedure)
			}
		}
	}
	for procedure := range rpcKinds {
		if !seen[procedure] {
			t.Errorf("classification %s does not match any RPC in the proto", procedure)
		}
	}
}

// rpcKindOverrides documents the RPCs whose name does not encode their
// direction. TestRPCKindMatchesVerb requires every such RPC to have an entry
// and requires the entry to agree with rpcKinds, so an ambiguous method cannot
// be classified silently.
var rpcKindOverrides = map[string]struct {
	kind   rpcKind
	reason string
}{
	"Generate":                     {rpcRead, "stateless upstream generation; it does not change daemon state"},
	"StreamAgentRun":               {rpcWrite, "creates a run and drives an agent"},
	"StreamExec":                   {rpcWrite, "executes a command that may mutate the sandbox"},
	"StreamProjectSchedulerEvents": {rpcRead, "read-only event stream"},
	"StreamSchedulerRuns":          {rpcRead, "read-only run stream"},
}

var readVerbPrefixes = []string{"Get", "List", "Watch", "Inspect", "Validate", "Resolve", "BatchGet", "Download", "Follow"}
var writeVerbPrefixes = []string{"Create", "Update", "Delete", "Remove", "Apply", "Patch", "Set", "Clear", "Prune", "Stop", "Start", "Run", "Invoke", "Pull", "Build", "Attach", "Exec", "Resume", "Abandon"}

// TestRPCKindMatchesVerb is a second, independent classification check. It
// cannot prove that a direction is semantically right, but it catches a
// copy-paste or typo error in the exhaustive table and forces every ambiguous
// method to state its rationale.
func TestRPCKindMatchesVerb(t *testing.T) {
	for procedure, kind := range rpcKinds {
		name := procedure[strings.LastIndex(procedure, "/")+1:]
		if override, ok := rpcKindOverrides[name]; ok {
			if strings.TrimSpace(override.reason) == "" {
				t.Errorf("override for %s has no rationale", name)
			}
			if kind != override.kind {
				t.Errorf("RPC %s is %v in rpcKinds but %v in rpcKindOverrides", name, kind, override.kind)
			}
			continue
		}
		expected, ok := kindFromVerb(name)
		if !ok {
			t.Errorf("RPC %s has no verb convention; add it to rpcKindOverrides with a rationale", name)
			continue
		}
		if kind != expected {
			t.Errorf("RPC %s is classified %v, but its verb implies %v", name, kind, expected)
		}
	}
}

func kindFromVerb(name string) (rpcKind, bool) {
	for _, prefix := range readVerbPrefixes {
		if strings.HasPrefix(name, prefix) {
			return rpcRead, true
		}
	}
	for _, prefix := range writeVerbPrefixes {
		if strings.HasPrefix(name, prefix) {
			return rpcWrite, true
		}
	}
	return rpcRead, false
}

// TestReasonCodesCoverEveryReason ties the reason table to the wire enum. A
// new OperationErrorReason fails this test until it has a Connect code, so it
// cannot silently fall through to CodeInternal.
func TestReasonCodesCoverEveryReason(t *testing.T) {
	for value, name := range agentcomposev2.OperationErrorReason_name {
		if value == int32(apierror.ReasonUnspecified) {
			continue
		}
		if _, ok := reasonCodes[apierror.Reason(value)]; !ok {
			t.Errorf("reason %s (%d) has no Connect code mapping", name, value)
		}
	}
}

// TestReasonCodeRoundTrip pins the two mappings against each other. It is not
// a perfect inverse, and the losses are deliberate:
//
//   - CodeAlreadyExists and CodeFailedPrecondition both collapse into
//     ReasonPreconditionFailed, which has no way back to AlreadyExists;
//   - a generic CodeUnavailable has no reason of its own and maps to
//     ReasonInternal.
//
// Pinning them here means a future change has to choose a new behavior
// explicitly instead of drifting.
func TestReasonCodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		code connect.Code
		kind ResourceKind
		want connect.Code
	}{
		{"canceled", connect.CodeCanceled, ResourceGeneric, connect.CodeCanceled},
		{"invalid argument", connect.CodeInvalidArgument, ResourceGeneric, connect.CodeInvalidArgument},
		{"deadline exceeded", connect.CodeDeadlineExceeded, ResourceGeneric, connect.CodeDeadlineExceeded},
		{"not found", connect.CodeNotFound, ResourceGeneric, connect.CodeNotFound},
		{"sandbox not found", connect.CodeNotFound, ResourceSandbox, connect.CodeNotFound},
		{"already exists collapses to failed precondition", connect.CodeAlreadyExists, ResourceGeneric, connect.CodeFailedPrecondition},
		{"failed precondition", connect.CodeFailedPrecondition, ResourceGeneric, connect.CodeFailedPrecondition},
		{"aborted", connect.CodeAborted, ResourceGeneric, connect.CodeAborted},
		{"sandbox unavailable", connect.CodeUnavailable, ResourceSandbox, connect.CodeUnavailable},
		{"image unavailable", connect.CodeUnavailable, ResourceImage, connect.CodeUnavailable},
		{"generic unavailable has no reason", connect.CodeUnavailable, ResourceGeneric, connect.CodeInternal},
		{"internal", connect.CodeInternal, ResourceGeneric, connect.CodeInternal},
		{"unimplemented", connect.CodeUnimplemented, ResourceGeneric, connect.CodeUnimplemented},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason := reasonForConnectCode(tc.code, tc.kind)
			if got := codeForReason(reason); got != tc.want {
				t.Errorf("codeForReason(reasonForConnectCode(%v, %v)) = %v, want %v", tc.code, tc.kind, got, tc.want)
			}
		})
	}
}
