package agentcomposev2

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestListRunsTimeFiltersUseTimestampFields(t *testing.T) {
	descriptor := (&ListRunsRequest{}).ProtoReflect().Descriptor()
	for _, name := range []protoreflect.Name{"started_from", "started_to"} {
		field := descriptor.Fields().ByName(name)
		if field == nil {
			t.Fatalf("field %s is missing", name)
		}
		if field.Kind() != protoreflect.MessageKind || field.Message().FullName() != "google.protobuf.Timestamp" {
			t.Fatalf("field %s type = %s, want google.protobuf.Timestamp", name, field.Kind())
		}
	}
}

func TestListRunsEventFilterField(t *testing.T) {
	descriptor := (&ListRunsRequest{}).ProtoReflect().Descriptor()
	field := descriptor.Fields().ByName("event_id")
	if field == nil {
		t.Fatalf("field event_id is missing")
	}
	if field.Kind() != protoreflect.StringKind {
		t.Fatalf("field event_id kind = %s, want string", field.Kind())
	}
	if got, want := field.Number(), protoreflect.FieldNumber(13); got != want {
		t.Fatalf("field event_id number = %d, want %d", got, want)
	}
}

func TestListRunsResponseEventScopeTruncatedField(t *testing.T) {
	descriptor := (&ListRunsResponse{}).ProtoReflect().Descriptor()
	field := descriptor.Fields().ByName("event_scope_truncated")
	if field == nil {
		t.Fatalf("field event_scope_truncated is missing")
	}
	if field.Kind() != protoreflect.BoolKind {
		t.Fatalf("field event_scope_truncated kind = %s, want bool", field.Kind())
	}
	if got, want := field.Number(), protoreflect.FieldNumber(3); got != want {
		t.Fatalf("field event_scope_truncated number = %d, want %d", got, want)
	}
}
