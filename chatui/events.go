package main

import (
	"github.com/chaitin/agent-compose/sdk/go/chat"
)

// maxToolOutput bounds what one tool result contributes to a frame. A build
// log can run to megabytes, and a browser that has to lay all of it out stops
// being able to show the turn it belongs to.
const maxToolOutput = 4000

// renderEvent flattens one agent event for the browser.
//
// Step is carried on every event that has one, because the UI groups a turn's
// work by step. Grouping by the interval between step_start and step_end would
// be wrong: providers interleave, and an event's own step number is the only
// thing that says which step it belongs to.
//
// A field the provider did not report is left out rather than sent as a zero,
// so the page can tell "did not happen" from "never reported".
func renderEvent(event chat.Event) map[string]any {
	payload := map[string]any{"kind": string(event.Kind()), "at": event.At()}
	switch typed := event.(type) {
	case *chat.StepStartEvent:
		putStep(payload, typed.Step)
	case *chat.StepEndEvent:
		putStep(payload, typed.Step)
		put(payload, "stopReason", string(typed.StopReason))
		put(payload, "rawStopReason", typed.RawStopReason)
	case *chat.TextDeltaEvent:
		putStep(payload, typed.Step)
		payload["text"] = typed.Text
	case *chat.ReasoningDeltaEvent:
		putStep(payload, typed.Step)
		payload["text"] = typed.Text
	case *chat.ToolCallEvent:
		putStep(payload, typed.Step)
		payload["id"] = typed.ID
		payload["name"] = typed.Name
		payload["toolKind"] = string(typed.ToolKind)
		payload["status"] = string(typed.Status)
		put(payload, "command", typed.Command)
		put(payload, "parentToolUseId", typed.ParentToolUseID)
		if len(typed.Input) > 0 {
			payload["input"] = typed.Input
		}
		if typed.ExitCode != nil {
			payload["exitCode"] = *typed.ExitCode
		}
		if len(typed.Changes) > 0 {
			payload["changes"] = typed.Changes
		}
	case *chat.ToolResultEvent:
		putStep(payload, typed.Step)
		payload["id"] = typed.ID
		payload["ok"] = typed.OK
		put(payload, "output", truncate(typed.Output, maxToolOutput))
		put(payload, "error", truncate(typed.Error, maxToolOutput))
	case *chat.TodoEvent:
		payload["items"] = typed.Items
	case *chat.UsageEvent:
		putStep(payload, typed.Step)
		// Scope travels with every usage record because records of differing
		// scope must never be summed: one provider reports per step, another
		// repeats a running total for the whole turn.
		payload["scope"] = string(typed.Scope)
		payload["inputTokens"] = typed.InputTokens
		payload["outputTokens"] = typed.OutputTokens
		put(payload, "model", typed.Model)
		if typed.ReasoningTokens != nil {
			payload["reasoningTokens"] = *typed.ReasoningTokens
		}
		if typed.CachedTokens != nil {
			payload["cachedTokens"] = *typed.CachedTokens
		}
		if typed.CacheWriteTokens != nil {
			payload["cacheWriteTokens"] = *typed.CacheWriteTokens
		}
		if typed.CostUSD != nil {
			payload["costUsd"] = *typed.CostUSD
		}
	case *chat.RetryEvent:
		payload["reason"] = string(typed.Reason)
		payload["attempt"] = typed.Attempt
		if typed.MaxAttempts != nil {
			payload["maxAttempts"] = *typed.MaxAttempts
		}
		put(payload, "message", typed.Message)
	case *chat.CompactionEvent:
		payload["phase"] = string(typed.Phase)
	case *chat.ErrorEvent:
		payload["severity"] = string(typed.Severity)
		payload["message"] = typed.Message
		put(payload, "code", typed.Code)
		if typed.Retryable != nil {
			payload["retryable"] = *typed.Retryable
		}
	}
	return payload
}

// putStep records the step an event belongs to, and records nothing when the
// provider does not number its steps.
func putStep(payload map[string]any, step *int) {
	if step != nil {
		payload["step"] = *step
	}
}

func put(payload map[string]any, key, value string) {
	if value != "" {
		payload[key] = value
	}
}
