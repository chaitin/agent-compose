package schedulers

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func SessionTopicPayload(session *domain.Sandbox, source string) map[string]any {
	if session == nil {
		return nil
	}
	return map[string]any{
		"sandboxId":     session.Summary.ID,
		"title":         session.Summary.Title,
		"driver":        session.Summary.Driver,
		"vmStatus":      session.Summary.VMStatus,
		"guestImage":    session.Summary.GuestImage,
		"triggerSource": session.Summary.TriggerSource,
		"source":        source,
	}
}

func CommandEventPayload(request domain.SchedulerCommandRequest, result domain.SchedulerCommandResult) map[string]any {
	payload := map[string]any{
		"mode":            strings.TrimSpace(request.Mode),
		"command":         strings.TrimSpace(request.Command),
		"args":            append([]string(nil), request.Args...),
		"cwd":             strings.TrimSpace(request.Cwd),
		"exitCode":        result.ExitCode,
		"success":         result.Success,
		"stdoutTruncated": result.StdoutTruncated,
		"stderrTruncated": result.StderrTruncated,
		"outputBytes":     commandOutputBytes(result),
		"sandboxId":       result.SandboxID,
		"cellId":          result.CellID,
	}
	if payload["mode"] == "shell" {
		payload["command"] = ""
	}
	return payload
}

// commandOutputBytes reports the size of whichever field addLinkedSchedulerEvent
// would have used as the scheduler_event.message before this event type started
// clearing it: the
// first non-blank of Output/Stdout/Stderr. It backs the "content unavailable"
// placeholder ResolveEventMessage produces when the artifact can't be read.
func commandOutputBytes(result domain.SchedulerCommandResult) int {
	switch {
	case strings.TrimSpace(result.Output) != "":
		return len(result.Output)
	case strings.TrimSpace(result.Stdout) != "":
		return len(result.Stdout)
	case strings.TrimSpace(result.Stderr) != "":
		return len(result.Stderr)
	default:
		return 0
	}
}
