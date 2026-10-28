package apierror

import (
	"testing"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestReasonNumbersMatchWireContract compares every transport-neutral constant
// with the protobuf enum the wire uses. This is deliberately a cross-source
// assertion: pinning each side to its own literal would let them drift apart
// while both tests still passed.
func TestReasonNumbersMatchWireContract(t *testing.T) {
	tests := []struct {
		name string
		got  Reason
		want agentcomposev2.OperationErrorReason
	}{
		{"Unspecified", ReasonUnspecified, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_UNSPECIFIED},
		{"ProjectSpecInvalid", ReasonProjectSpecInvalid, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_PROJECT_SPEC_INVALID},
		{"AgentDefinitionInvalid", ReasonAgentDefinitionInvalid, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_AGENT_DEFINITION_INVALID},
		{"SchedulerInvalid", ReasonSchedulerInvalid, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SCHEDULER_INVALID},
		{"SchedulerScriptInvalid", ReasonSchedulerScriptInvalid, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SCHEDULER_SCRIPT_INVALID},
		{"SchedulerReconcileFailed", ReasonSchedulerReconcileFailed, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SCHEDULER_RECONCILE_FAILED},
		{"NetworkPolicyRejected", ReasonNetworkPolicyRejected, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_NETWORK_POLICY_REJECTED},
		{"ProjectNotFound", ReasonProjectNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_PROJECT_NOT_FOUND},
		{"RevisionConflict", ReasonRevisionConflict, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_REVISION_CONFLICT},
		{"InvalidRequest", ReasonInvalidRequest, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_INVALID_REQUEST},
		{"Unsupported", ReasonUnsupported, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_UNSUPPORTED},
		{"OperationBusy", ReasonOperationBusy, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_OPERATION_BUSY},
		{"PreconditionFailed", ReasonPreconditionFailed, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_PRECONDITION_FAILED},
		{"SchedulerNotFound", ReasonSchedulerNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SCHEDULER_NOT_FOUND},
		{"SchedulerRunNotFound", ReasonSchedulerRunNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SCHEDULER_RUN_NOT_FOUND},
		{"RunNotFound", ReasonRunNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_RUN_NOT_FOUND},
		{"SandboxNotFound", ReasonSandboxNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SANDBOX_NOT_FOUND},
		{"ImageNotFound", ReasonImageNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_NOT_FOUND},
		{"ImagePullFailed", ReasonImagePullFailed, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_PULL_FAILED},
		{"ImageReferenceInvalid", ReasonImageReferenceInvalid, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_REFERENCE_INVALID},
		{"ImageBackendUnavailable", ReasonImageBackendUnavailable, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_BACKEND_UNAVAILABLE},
		{"ImageBuildFailed", ReasonImageBuildFailed, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_BUILD_FAILED},
		{"ImageInUse", ReasonImageInUse, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_IMAGE_IN_USE},
		{"VolumeNotFound", ReasonVolumeNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_VOLUME_NOT_FOUND},
		{"VolumeUnavailable", ReasonVolumeUnavailable, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_VOLUME_UNAVAILABLE},
		{"SandboxCreationFailed", ReasonSandboxCreationFailed, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SANDBOX_CREATION_FAILED},
		{"SandboxStopFailed", ReasonSandboxStopFailed, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SANDBOX_STOP_FAILED},
		{"SandboxRuntimeUnavailable", ReasonSandboxRuntimeUnavail, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_SANDBOX_RUNTIME_UNAVAILABLE},
		{"StorageUnavailable", ReasonStorageUnavailable, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_STORAGE_UNAVAILABLE},
		{"Internal", ReasonInternal, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_INTERNAL},
		{"OperationCanceled", ReasonOperationCanceled, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_OPERATION_CANCELED},
		{"OperationTimeout", ReasonOperationTimeout, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_OPERATION_TIMEOUT},
		{"ResourceNotFound", ReasonResourceNotFound, agentcomposev2.OperationErrorReason_OPERATION_ERROR_REASON_RESOURCE_NOT_FOUND},
	}
	for _, tc := range tests {
		if int32(tc.got) != int32(tc.want) {
			t.Errorf("%s = %d, proto = %d", tc.name, tc.got, tc.want)
		}
	}
}

func TestEffectNumbersMatchWireContract(t *testing.T) {
	tests := []struct {
		name string
		got  Effect
		want agentcomposev2.OperationEffect
	}{
		{"Unspecified", EffectUnspecified, agentcomposev2.OperationEffect_OPERATION_EFFECT_UNSPECIFIED},
		{"NotApplied", EffectNotApplied, agentcomposev2.OperationEffect_OPERATION_EFFECT_NOT_APPLIED},
		{"Unknown", EffectUnknown, agentcomposev2.OperationEffect_OPERATION_EFFECT_UNKNOWN},
	}
	for _, tc := range tests {
		if int32(tc.got) != int32(tc.want) {
			t.Errorf("%s = %d, proto = %d", tc.name, tc.got, tc.want)
		}
	}
}
