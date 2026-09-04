package main

import (
	"cmp"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

func renderEvent(event chat.Event) map[string]any {
	payload := map[string]any{"kind": string(event.Kind())}
	switch typed := event.(type) {
	case *chat.TextDeltaEvent:
		payload["text"] = typed.Text
	case *chat.ReasoningDeltaEvent:
		payload["text"] = typed.Text
	case *chat.ToolCallEvent:
		payload["id"] = typed.ID
		payload["name"] = typed.Name
		payload["toolKind"] = typed.ToolKind
		payload["status"] = typed.Status
		payload["command"] = typed.Command
	case *chat.ToolResultEvent:
		payload["id"] = typed.ID
		payload["ok"] = typed.OK
		payload["output"] = truncate(cmp.Or(typed.Output, typed.Error), 2000)
	case *chat.TodoEvent:
		payload["items"] = typed.Items
	case *chat.UsageEvent:
		payload["scope"] = typed.Scope
		payload["inputTokens"] = typed.InputTokens
		payload["outputTokens"] = typed.OutputTokens
	case *chat.RetryEvent:
		payload["reason"] = typed.Reason
		payload["attempt"] = typed.Attempt
	case *chat.ErrorEvent:
		payload["severity"] = typed.Severity
		payload["message"] = typed.Message
	case *chat.StepEndEvent:
		payload["stopReason"] = typed.StopReason
	}
	return payload
}

// stop interrupts the turn in flight.
//
// The daemon cancels the whole session rather than one turn, so the
// conversation restarts on the next message. The response says so instead of
// letting the UI pretend the agent kept its context.
