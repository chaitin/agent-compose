package execution

import (
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func TestParseDetachCapabilities(t *testing.T) {
	t.Run("supports detach", func(t *testing.T) {
		caps, ok, err := ParseDetachCapabilities(`{"detach":true}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok || !caps.Detach {
			t.Fatalf("expected detach=true, got ok=%v caps=%+v", ok, caps)
		}
	})
	t.Run("unknown fields are ignored", func(t *testing.T) {
		caps, ok, err := ParseDetachCapabilities(`{"detach":true,"future":1}` + "\n")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok || !caps.Detach {
			t.Fatalf("expected detach=true, got ok=%v caps=%+v", ok, caps)
		}
	})
	t.Run("empty output is not supported", func(t *testing.T) {
		_, ok, err := ParseDetachCapabilities("")
		if err != nil || ok {
			t.Fatalf("expected ok=false, got ok=%v err=%v", ok, err)
		}
	})
	t.Run("non-json output is not supported", func(t *testing.T) {
		_, ok, err := ParseDetachCapabilities("command not found")
		if err != nil || ok {
			t.Fatalf("expected ok=false, got ok=%v err=%v", ok, err)
		}
	})
}

func TestParseDetachAttachLine(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		want    DetachAttachRecord
		wantErr bool
	}{
		{"out", `{"t":"out","s":"stdout","text":"hi\n","offset":4}`, DetachAttachRecord{Type: "out", Stream: "stdout", Text: "hi\n", Offset: 4}, false},
		{"gap", `{"t":"gap","from":10,"to":20}`, DetachAttachRecord{Type: "gap", From: 10, To: 20}, false},
		{"exit", `{"t":"exit","code":0,"reason":"completed"}`, DetachAttachRecord{Type: "exit", Code: 0, Reason: "completed"}, false},
		{"exit stopped", `{"t":"exit","code":143,"reason":"stopped","stopReason":"user"}`, DetachAttachRecord{Type: "exit", Code: 143, Reason: "stopped", StopReason: "user"}, false},
		{"exit abnormal with detail", `{"t":"exit","code":-1,"reason":"abnormal","detail":"EISDIR: illegal operation on a directory"}`, DetachAttachRecord{Type: "exit", Code: -1, Reason: "abnormal", Detail: "EISDIR: illegal operation on a directory"}, false},
		{"unknown type", `{"t":"nope"}`, DetachAttachRecord{}, true},
		{"malformed", `not json`, DetachAttachRecord{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDetachAttachLine(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestExecResultFromDetachExit(t *testing.T) {
	tests := []struct {
		name       string
		exit       DetachExitResult
		wantCode   int
		wantErr    bool
		wantCancel bool
	}{
		{"completed zero", DetachExitResult{ExitCode: 0, Reason: DetachExitCompleted}, 0, false, false},
		{"completed non-zero", DetachExitResult{ExitCode: 3, Reason: DetachExitCompleted}, 3, false, false},
		{"stopped user", DetachExitResult{ExitCode: 143, Reason: DetachExitStopped, StopReason: DetachStopReasonUser}, 143, true, false},
		{"stopped timeout", DetachExitResult{ExitCode: 124, Reason: DetachExitStopped, StopReason: DetachStopReasonTimeout}, 124, true, false},
		{"signal", DetachExitResult{ExitCode: 137, Reason: DetachExitSignal, Signal: "KILL"}, 137, true, false},
		{"abnormal", DetachExitResult{ExitCode: -1, Reason: DetachExitAbnormal}, -1, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ExecResultFromDetachExit(tc.exit)
			if result.ExitCode != tc.wantCode {
				t.Fatalf("exit code got %d want %d", result.ExitCode, tc.wantCode)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("err got %v want %v", err, tc.wantErr)
			}
		})
	}
}

func TestExecResultFromDetachExitSurfacesAbnormalDetail(t *testing.T) {
	_, err := ExecResultFromDetachExit(DetachExitResult{
		ExitCode: -1,
		Reason:   DetachExitAbnormal,
		Detail:   "EISDIR: illegal operation on a directory",
	})
	if err == nil || !strings.Contains(err.Error(), "EISDIR") {
		t.Fatalf("err = %v, want the abnormal detail surfaced", err)
	}
	// Without a detail the error still names the fallback cause.
	_, err = ExecResultFromDetachExit(DetachExitResult{ExitCode: -1, Reason: DetachExitAbnormal})
	if err == nil || !strings.Contains(err.Error(), "supervisor gone without an exit record") {
		t.Fatalf("err = %v, want the fallback abnormal cause", err)
	}
}

func TestBuildDetachSpecs(t *testing.T) {
	config := &appconfig.Config{}
	appconfig.ApplyDefaultGuestPaths(config)

	t.Run("start carries execution id marker and task args", func(t *testing.T) {
		spec := BuildDetachStartSpec(config, "exec-1", DetachStartOptions{}, []string{"exec", "--request-file", "/x"})
		if spec.Marker != "exec-1" {
			t.Fatalf("marker got %q want exec-1", spec.Marker)
		}
		if !spec.KillOnlyOnCancel {
			t.Fatalf("start spec must set KillOnlyOnCancel so a shutdown does not kill the detached task tree")
		}
		if spec.Command != "sh" {
			t.Fatalf("command got %q", spec.Command)
		}
		joined := strings.Join(spec.Args, " ")
		for _, want := range []string{"start", "--detach", "--execution-id", "exec-1", "exec", "--request-file"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("args %q missing %q", joined, want)
			}
		}
	})

	t.Run("start hands a positive timeout to the supervisor before the task args", func(t *testing.T) {
		spec := BuildDetachStartSpec(config, "exec-1", DetachStartOptions{TaskTimeout: 90 * time.Second, Stdin: true}, []string{"exec"})
		joined := strings.Join(spec.Args, " ")
		timeoutAt := strings.Index(joined, "'--task-timeout-ms' '90000'")
		separatorAt := strings.Index(joined, "'--' 'exec'")
		if timeoutAt < 0 || separatorAt < 0 || timeoutAt > separatorAt {
			t.Fatalf("args %q want --task-timeout-ms 90000 before the task separator", joined)
		}
		if !strings.Contains(joined, "'--stdin' '--'") {
			t.Fatalf("args %q want --stdin before the task separator", joined)
		}
		if noTimeout := strings.Join(BuildDetachStartSpec(config, "exec-1", DetachStartOptions{}, []string{"exec"}).Args, " "); strings.Contains(noTimeout, "--task-timeout-ms") {
			t.Fatalf("args %q must omit --task-timeout-ms without a timeout", noTimeout)
		}
	})

	t.Run("cleanup targets the execution", func(t *testing.T) {
		spec := BuildDetachCleanupSpec(config, "exec-1")
		joined := strings.Join(spec.Args, " ")
		if !strings.HasPrefix(joined, "cleanup --execution-id exec-1") {
			t.Fatalf("args %q", joined)
		}
	})

	t.Run("attach is kill-only and resumes from offset", func(t *testing.T) {
		spec := BuildDetachAttachSpec(config, "exec-1", 42)
		if !spec.KillOnlyOnCancel {
			t.Fatalf("attach spec must set KillOnlyOnCancel")
		}
		if spec.Marker != "" {
			t.Fatalf("attach spec must not carry a marker, got %q", spec.Marker)
		}
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "--from 42") {
			t.Fatalf("args %q missing --from 42", joined)
		}
	})

	t.Run("stop carries reason", func(t *testing.T) {
		spec := BuildDetachStopSpec(config, "exec-1", DetachStopReasonUser)
		joined := strings.Join(spec.Args, " ")
		if !strings.Contains(joined, "--reason user") {
			t.Fatalf("args %q missing --reason user", joined)
		}
	})
}
