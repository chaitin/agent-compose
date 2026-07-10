package driver

import (
	"context"
	"errors"
	"io"
	"testing"
)

type guardedInteractionStub struct {
	closeCalls int
}

func (s *guardedInteractionStub) Send(RuntimeInputFrame) error { return nil }
func (s *guardedInteractionStub) CloseSend() error             { s.closeCalls++; return nil }
func (s *guardedInteractionStub) Recv() (RuntimeOutputFrame, error) {
	return RuntimeOutputFrame{}, io.EOF
}
func (s *guardedInteractionStub) Wait() (RuntimeResult, error) { return RuntimeResult{}, nil }

func TestGuardRuntimeInteractionInputClosesOnceAndRejectsSend(t *testing.T) {
	stub := &guardedInteractionStub{}
	interaction := GuardRuntimeInteractionInput(stub)
	if err := interaction.CloseSend(); err != nil {
		t.Fatalf("CloseSend() error = %v", err)
	}
	if err := interaction.CloseSend(); err != nil {
		t.Fatalf("second CloseSend() error = %v", err)
	}
	if stub.closeCalls != 1 {
		t.Fatalf("underlying CloseSend() calls = %d, want 1", stub.closeCalls)
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdin}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Send() after close error = %v, want io.ErrClosedPipe", err)
	}
}

func TestRuntimeInteractionCapabilitiesValidateStartSpec(t *testing.T) {
	caps := RuntimeInteractionCapabilities{
		NativeExec: true,
		Stdin:      true,
		StdinEOF:   true,
		TTY:        true,
		Resize:     true,
		Artifacts:  true,
	}
	spec := RuntimeStartSpec{
		Kind:        RuntimeOperationCommand,
		AttachStdin: true,
		TTY:         true,
		Rows:        24,
		Cols:        80,
		ArtifactDir: "/tmp/artifacts",
	}
	if err := caps.ValidateStartSpec(RuntimeDriverDocker, spec); err != nil {
		t.Fatalf("ValidateStartSpec() error = %v", err)
	}
}

func TestRuntimeInteractionCapabilitiesValidateStartSpecUnsupported(t *testing.T) {
	spec := RuntimeStartSpec{Kind: RuntimeOperationCommand, AttachStdin: true}
	err := (RuntimeInteractionCapabilities{}).ValidateStartSpec(RuntimeDriverDocker, spec)
	if !errors.Is(err, ErrRuntimeInteractionUnsupported) {
		t.Fatalf("ValidateStartSpec() error = %v, want ErrRuntimeInteractionUnsupported", err)
	}
	var unsupported *RuntimeInteractionUnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("ValidateStartSpec() error = %T, want RuntimeInteractionUnsupportedError", err)
	}
	if unsupported.Driver != RuntimeDriverDocker {
		t.Fatalf("unsupported.Driver = %q, want %q", unsupported.Driver, RuntimeDriverDocker)
	}
	if unsupported.Capability != RuntimeCapabilityNativeExec {
		t.Fatalf("unsupported.Capability = %q, want %q", unsupported.Capability, RuntimeCapabilityNativeExec)
	}
}

func TestBoxliteAndMicrosandboxNativeExecAttachUnsupported(t *testing.T) {
	spec := RuntimeStartSpec{Kind: RuntimeOperationCommand, AttachStdin: true, TTY: true}
	for _, driver := range []string{RuntimeDriverBoxlite, RuntimeDriverMicrosandbox} {
		t.Run(driver, func(t *testing.T) {
			_, err := UnsupportedRuntimeInteraction(driver, RuntimeInteractionCapabilities{}, spec)
			if !errors.Is(err, ErrRuntimeInteractionUnsupported) {
				t.Fatalf("OpenInteraction() error = %v, want ErrRuntimeInteractionUnsupported", err)
			}
			var unsupported *RuntimeInteractionUnsupportedError
			if !errors.As(err, &unsupported) {
				t.Fatalf("OpenInteraction() error = %T, want RuntimeInteractionUnsupportedError", err)
			}
			if unsupported.Driver != driver || unsupported.Operation != RuntimeOperationCommand || unsupported.Capability != RuntimeCapabilityNativeExec {
				t.Fatalf("unsupported = %#v, want driver=%q operation=%q capability=%q", unsupported, driver, RuntimeOperationCommand, RuntimeCapabilityNativeExec)
			}
		})
	}
}

func TestExecStreamInteractionProjectsLegacyFrames(t *testing.T) {
	runtime := fakeInteractionRuntime{}
	interaction := NewExecStreamInteraction(context.Background(), runtime, nil, VMState{}, RuntimeStartSpec{
		OperationID: "op-1",
		Kind:        RuntimeOperationCommand,
		Command: &RuntimeCommandSpec{
			Command: "echo",
			Args:    []string{"ok"},
		},
	})

	frame, err := interaction.Recv()
	if err != nil {
		t.Fatalf("Recv() started error = %v", err)
	}
	if frame.Type != RuntimeOutputStarted {
		t.Fatalf("first frame type = %q, want %q", frame.Type, RuntimeOutputStarted)
	}

	frame, err = interaction.Recv()
	if err != nil {
		t.Fatalf("Recv() stdout error = %v", err)
	}
	if frame.Type != RuntimeOutputStdout || string(frame.Data) != "hello\n" {
		t.Fatalf("stdout frame = %#v", frame)
	}

	frame, err = interaction.Recv()
	if err != nil {
		t.Fatalf("Recv() stderr error = %v", err)
	}
	if frame.Type != RuntimeOutputStderr || string(frame.Data) != "warn\n" {
		t.Fatalf("stderr frame = %#v", frame)
	}

	frame, err = interaction.Recv()
	if err != nil {
		t.Fatalf("Recv() result error = %v", err)
	}
	if frame.Type != RuntimeOutputResult || frame.Result == nil || !frame.Result.Success || frame.Result.OperationID != "op-1" {
		t.Fatalf("result frame = %#v", frame)
	}

	if _, err := interaction.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after close error = %v, want EOF", err)
	}
	result, err := interaction.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !result.Success || result.ExitCode != 0 || result.OperationID != "op-1" {
		t.Fatalf("Wait() result = %#v", result)
	}
}

type fakeInteractionRuntime struct{}

func (fakeInteractionRuntime) EnsureSandbox(context.Context, *Sandbox, VMState, ProxyState) (SandboxVMInfo, error) {
	return SandboxVMInfo{}, nil
}

func (fakeInteractionRuntime) StopSandbox(context.Context, *Sandbox, VMState) (bool, error) {
	return true, nil
}

func (fakeInteractionRuntime) RemoveSandbox(context.Context, *Sandbox, VMState) error {
	return nil
}

func (fakeInteractionRuntime) Exec(context.Context, *Sandbox, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, nil
}

func (fakeInteractionRuntime) ExecStream(ctx context.Context, session *Sandbox, vmState VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	stream(ExecChunk{Text: "hello\n", Stream: StdioStdout})
	stream(ExecChunk{Text: "warn\n", Stream: StdioStderr})
	return ExecResult{ExitCode: 0, Success: true}, nil
}
