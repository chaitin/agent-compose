package agentcomposev2

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestOperationErrorReasonIsFrozen pins every numeric value. Reason values are
// a published machine contract: renumbering one silently changes what an old
// client believes, so the only permitted change is appending a new value at
// the end.
func TestOperationErrorReasonIsFrozen(t *testing.T) {
	want := map[protoreflect.Name]protoreflect.EnumNumber{
		"OPERATION_ERROR_REASON_UNSPECIFIED":                 0,
		"OPERATION_ERROR_REASON_PROJECT_SPEC_INVALID":        1,
		"OPERATION_ERROR_REASON_AGENT_DEFINITION_INVALID":    2,
		"OPERATION_ERROR_REASON_SCHEDULER_INVALID":           3,
		"OPERATION_ERROR_REASON_SCHEDULER_SCRIPT_INVALID":    4,
		"OPERATION_ERROR_REASON_SCHEDULER_RECONCILE_FAILED":  5,
		"OPERATION_ERROR_REASON_NETWORK_POLICY_REJECTED":     6,
		"OPERATION_ERROR_REASON_PROJECT_NOT_FOUND":           7,
		"OPERATION_ERROR_REASON_REVISION_CONFLICT":           8,
		"OPERATION_ERROR_REASON_INVALID_REQUEST":             9,
		"OPERATION_ERROR_REASON_UNSUPPORTED":                 10,
		"OPERATION_ERROR_REASON_OPERATION_BUSY":              11,
		"OPERATION_ERROR_REASON_PRECONDITION_FAILED":         12,
		"OPERATION_ERROR_REASON_SCHEDULER_NOT_FOUND":         13,
		"OPERATION_ERROR_REASON_SCHEDULER_RUN_NOT_FOUND":     14,
		"OPERATION_ERROR_REASON_RUN_NOT_FOUND":               15,
		"OPERATION_ERROR_REASON_SANDBOX_NOT_FOUND":           16,
		"OPERATION_ERROR_REASON_IMAGE_NOT_FOUND":             17,
		"OPERATION_ERROR_REASON_IMAGE_PULL_FAILED":           18,
		"OPERATION_ERROR_REASON_IMAGE_REFERENCE_INVALID":     19,
		"OPERATION_ERROR_REASON_IMAGE_BACKEND_UNAVAILABLE":   20,
		"OPERATION_ERROR_REASON_IMAGE_BUILD_FAILED":          21,
		"OPERATION_ERROR_REASON_IMAGE_IN_USE":                22,
		"OPERATION_ERROR_REASON_VOLUME_NOT_FOUND":            23,
		"OPERATION_ERROR_REASON_VOLUME_UNAVAILABLE":          24,
		"OPERATION_ERROR_REASON_SANDBOX_CREATION_FAILED":     25,
		"OPERATION_ERROR_REASON_SANDBOX_STOP_FAILED":         26,
		"OPERATION_ERROR_REASON_SANDBOX_RUNTIME_UNAVAILABLE": 27,
		"OPERATION_ERROR_REASON_STORAGE_UNAVAILABLE":         28,
		"OPERATION_ERROR_REASON_INTERNAL":                    29,
		"OPERATION_ERROR_REASON_OPERATION_CANCELED":          30,
		"OPERATION_ERROR_REASON_OPERATION_TIMEOUT":           31,
		"OPERATION_ERROR_REASON_RESOURCE_NOT_FOUND":          32,
	}

	enum := File_agentcompose_v2_errors_proto.Enums().ByName("OperationErrorReason")
	if enum == nil {
		t.Fatal("OperationErrorReason enum not found")
	}
	values := enum.Values()
	if values.Len() != len(want) {
		t.Fatalf("OperationErrorReason has %d values, want %d", values.Len(), len(want))
	}
	for index := 0; index < values.Len(); index++ {
		value := values.Get(index)
		number, ok := want[value.Name()]
		if !ok {
			t.Errorf("unexpected reason %s = %d", value.Name(), value.Number())
			continue
		}
		if value.Number() != number {
			t.Errorf("%s = %d, want %d", value.Name(), value.Number(), number)
		}
		if value.Number() != protoreflect.EnumNumber(index) {
			t.Errorf("%s = %d is not contiguous at index %d", value.Name(), value.Number(), index)
		}
	}
}

// TestOperationEffectLeavesAppliedFree pins the v1 effect vocabulary and
// documents that value 3 is intentionally unused, not reserved.
func TestOperationEffectLeavesAppliedFree(t *testing.T) {
	enum := File_agentcompose_v2_errors_proto.Enums().ByName("OperationEffect")
	if enum == nil {
		t.Fatal("OperationEffect enum not found")
	}
	want := map[protoreflect.Name]protoreflect.EnumNumber{
		"OPERATION_EFFECT_UNSPECIFIED": 0,
		"OPERATION_EFFECT_NOT_APPLIED": 1,
		"OPERATION_EFFECT_UNKNOWN":     2,
	}
	if enum.Values().Len() != len(want) {
		t.Fatalf("OperationEffect has %d values, want %d", enum.Values().Len(), len(want))
	}
	for name, number := range want {
		value := enum.Values().ByName(name)
		if value == nil || value.Number() != number {
			t.Errorf("OperationEffect %s = %v, want %d", name, value, number)
		}
	}
	if enum.Values().ByNumber(3) != nil {
		t.Error("OperationEffect value 3 must stay free for a future APPLIED")
	}
}

// TestOperationErrorFieldsAreFrozen pins the field numbers added by the
// contract so a future field cannot silently reuse one.
func TestOperationErrorFieldsAreFrozen(t *testing.T) {
	detail := File_agentcompose_v2_errors_proto.Messages().ByName("OperationErrorDetail")
	if detail == nil {
		t.Fatal("OperationErrorDetail message not found")
	}
	for name, number := range map[protoreflect.Name]protoreflect.FieldNumber{"reason": 1, "effect": 2} {
		field := detail.Fields().ByName(name)
		if field == nil || field.Number() != number {
			t.Errorf("OperationErrorDetail.%s = %v, want %d", name, field, number)
		}
	}

	messages := File_agentcompose_v2_agentcompose_proto.Messages()
	for _, check := range []struct {
		message protoreflect.Name
		field   protoreflect.Name
		number  protoreflect.FieldNumber
	}{
		{"ProjectValidationIssue", "reason", 4},
		{"ApplyProjectResponse", "effect", 8},
		{"RemoveProjectResponse", "effect", 3},
		{"RemoveProjectResponse", "reason", 4},
	} {
		message := messages.ByName(check.message)
		if message == nil {
			t.Fatalf("message %s not found", check.message)
		}
		field := message.Fields().ByName(check.field)
		if field == nil || field.Number() != check.number {
			t.Errorf("%s.%s = %v, want %d", check.message, check.field, field, check.number)
		}
	}
}
