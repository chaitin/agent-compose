package chat

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDecodeEventKeepsAbsentFieldsAbsent(t *testing.T) {
	at := time.Unix(1000, 0).UTC()

	// A provider that does not report step numbers must not be made to look
	// like one reporting step 0.
	event, err := decodeEvent("text_delta", []byte(`{"text":"hi"}`), at)
	if err != nil {
		t.Fatalf("decode text_delta: %v", err)
	}
	delta, ok := event.(*TextDeltaEvent)
	if !ok {
		t.Fatalf("event = %T, want *TextDeltaEvent", event)
	}
	if delta.Step != nil {
		t.Errorf("step = %d, want nil for a provider that does not report it", *delta.Step)
	}
	if delta.Text != "hi" || delta.At() != at {
		t.Errorf("event = %#v", delta)
	}

	event, err = decodeEvent("text_delta", []byte(`{"text":"hi","step":0}`), at)
	if err != nil {
		t.Fatalf("decode text_delta with step: %v", err)
	}
	if step := event.(*TextDeltaEvent).Step; step == nil || *step != 0 {
		t.Errorf("step = %v, want an explicit 0", step)
	}
}

func TestDecodeEventTreatsLegacyOutputAsTextDelta(t *testing.T) {
	event, err := decodeEvent("output", []byte(`{"provider":"claude","text":"partial"}`), time.Unix(1000, 0).UTC())
	if err != nil {
		t.Fatalf("decode legacy output: %v", err)
	}
	delta, ok := event.(*TextDeltaEvent)
	if !ok {
		t.Fatalf("event = %T, want *TextDeltaEvent", event)
	}
	if delta.Text != "partial" {
		t.Errorf("text = %q, want partial", delta.Text)
	}
}

func TestDecodeEventVariants(t *testing.T) {
	at := time.Unix(1000, 0).UTC()
	for _, testCase := range []struct {
		name    string
		kind    string
		payload string
		verify  func(*testing.T, Event)
	}{
		{
			name:    "tool call",
			kind:    "tool_call",
			payload: `{"id":"t1","name":"bash","toolKind":"execute","status":"completed","command":"ls","exitCode":0,"input":{"cmd":"ls"}}`,
			verify: func(t *testing.T, event Event) {
				call := event.(*ToolCallEvent)
				if call.ToolKind != ToolExecute || call.Status != ToolCompleted || call.Command != "ls" {
					t.Errorf("call = %#v", call)
				}
				if call.ExitCode == nil || *call.ExitCode != 0 {
					t.Errorf("exit code = %v, want an explicit 0", call.ExitCode)
				}
				if string(call.Input) != `{"cmd":"ls"}` {
					t.Errorf("input = %s, want the raw payload preserved", call.Input)
				}
			},
		},
		{
			name:    "tool result",
			kind:    "tool_result",
			payload: `{"id":"t1","ok":false,"error":"boom"}`,
			verify: func(t *testing.T, event Event) {
				result := event.(*ToolResultEvent)
				if result.ID != "t1" || result.OK || result.Error != "boom" {
					t.Errorf("result = %#v", result)
				}
			},
		},
		{
			name:    "usage",
			kind:    "usage",
			payload: `{"scope":"turn","inputTokens":120,"outputTokens":30,"cachedTokens":90}`,
			verify: func(t *testing.T, event Event) {
				usage := event.(*UsageEvent)
				if usage.Scope != ScopeTurn || usage.InputTokens != 120 || usage.OutputTokens != 30 {
					t.Errorf("usage = %#v", usage)
				}
				if usage.CachedTokens == nil || *usage.CachedTokens != 90 {
					t.Errorf("cached tokens = %v, want 90", usage.CachedTokens)
				}
				if usage.CostUSD != nil {
					t.Errorf("cost = %v, want nil for a provider that does not price runs", *usage.CostUSD)
				}
			},
		},
		{
			name:    "step end",
			kind:    "step_end",
			payload: `{"step":2,"scope":"step","stopReason":"tool_use","rawStopReason":"toolUse"}`,
			verify: func(t *testing.T, event Event) {
				end := event.(*StepEndEvent)
				if end.Scope != StepEndScopeStep || end.StopReason != StopToolUse || end.RawStopReason != "toolUse" {
					t.Errorf("step end = %#v", end)
				}
			},
		},
		{
			name:    "run end",
			kind:    "step_end",
			payload: `{"scope":"run","stopReason":"stop"}`,
			verify: func(t *testing.T, event Event) {
				end := event.(*StepEndEvent)
				if end.Scope != StepEndScopeRun || end.Step != nil || end.StopReason != StopEnd {
					t.Errorf("run end = %#v", end)
				}
			},
		},
		{
			name:    "todo",
			kind:    "todo",
			payload: `{"items":[{"text":"read the diff","completed":true}]}`,
			verify: func(t *testing.T, event Event) {
				todo := event.(*TodoEvent)
				if len(todo.Items) != 1 || !todo.Items[0].Completed {
					t.Errorf("todo = %#v", todo)
				}
			},
		},
		{
			name:    "error",
			kind:    "error",
			payload: `{"severity":"warning","message":"slow","retryable":true}`,
			verify: func(t *testing.T, event Event) {
				failure := event.(*ErrorEvent)
				if failure.Severity != SeverityWarning || failure.Retryable == nil || !*failure.Retryable {
					t.Errorf("error = %#v", failure)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			event, err := decodeEvent(testCase.kind, []byte(testCase.payload), at)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if event == nil {
				t.Fatal("decode returned no event")
			}
			if string(event.Kind()) != testCase.kind {
				t.Errorf("kind = %q, want %q", event.Kind(), testCase.kind)
			}
			if event.At() != at {
				t.Errorf("time = %v, want %v", event.At(), at)
			}
			testCase.verify(t, event)
		})
	}
}

func TestDecodeEventPreservesKindsThisBuildDoesNotKnow(t *testing.T) {
	event, err := decodeEvent("some_future_kind", []byte(`{"whatever":1}`), time.Now())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	raw, ok := event.(*RawEvent)
	if !ok || raw.Name != "some_future_kind" || raw.PayloadJSON != `{"whatever":1}` {
		t.Errorf("event = %#v, want preserved raw event", event)
	}
}

func TestErrorUnwrapsToSentinels(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		err    *Error
		target error
	}{
		{"code wins over status", &Error{Code: "not_found", Status: http.StatusInternalServerError}, ErrNotFound},
		{"permission from code", &Error{Code: "permission_denied"}, ErrPermission},
		{"invalid from code", &Error{Code: "invalid_argument"}, ErrInvalidArgument},
		{"unavailable from code", &Error{Code: "unavailable"}, ErrUnavailable},
		{"status when no code", &Error{Status: http.StatusForbidden}, ErrPermission},
		{"gateway status", &Error{Status: http.StatusBadGateway}, ErrUnavailable},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if !errors.Is(testCase.err, testCase.target) {
				t.Errorf("%v does not match %v", testCase.err, testCase.target)
			}
		})
	}
}

func TestNewRejectsUnusableBaseURLs(t *testing.T) {
	for _, testCase := range []struct{ name, baseURL string }{
		{"empty", ""},
		{"relative", "/agentcompose"},
		{"unsupported scheme", "unix:///var/run/agent-compose.sock"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := New(Config{BaseURL: testCase.baseURL}); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("New(%q) error = %v, want ErrInvalidArgument", testCase.baseURL, err)
			}
		})
	}
	if _, err := New(Config{BaseURL: "http://127.0.0.1:7410/"}); err != nil {
		t.Errorf("New: %v", err)
	}
}

func TestWithLabelsCannotOverrideConversationIdentity(t *testing.T) {
	resolved := newOptions([]Option{
		WithID("conv-1"),
		WithLabels(map[string]string{"team": "platform", conversationLabel: "someone-elses"}),
	})
	if resolved.labels[conversationLabel] != "" {
		t.Errorf("labels = %v, want the identity label reserved", resolved.labels)
	}
	if resolved.labels["team"] != "platform" {
		t.Errorf("labels = %v, want the caller's own labels kept", resolved.labels)
	}
}

func TestConversationIDsAreGeneratedWhenNotSupplied(t *testing.T) {
	first, second := newOptions(nil).id, newOptions(nil).id
	if first == "" || first == second {
		t.Errorf("generated IDs %q and %q, want two distinct values", first, second)
	}
}
