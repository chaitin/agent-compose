//go:build cgo

package driver

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestDockerStopResumePreservesWritableLayerSmoke(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverDocker)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverDocker)
	runtime := &dockerRuntime{config: config}
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverDocker)
	assertRuntimeStopResumePreservesWritableLayer(t, ctx, config, runtime, session, vmState, proxyState)
}

func TestDockerCommandInteractionSmokeCatStdinEOF(t *testing.T) {
	runtimeSmokeEnabled(t, RuntimeDriverDocker)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config := newRuntimeSmokeConfig(t, RuntimeDriverDocker)
	runtime := &dockerRuntime{config: config}
	session, vmState, proxyState := newRuntimeSmokeSandbox(t, ctx, config, RuntimeDriverDocker)
	proxyState.Enabled = false

	info, err := runtime.EnsureSandbox(ctx, session, vmState, proxyState)
	if err != nil {
		t.Fatalf("EnsureSandbox() error = %v", err)
	}
	vmState.BoxID = info.BoxID
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), SandboxStopContextTimeout(RuntimeDriverDocker, config.SandboxStopTimeout))
		defer stopCancel()
		_, _ = runtime.StopSandbox(stopCtx, session, vmState)
	})

	interaction, err := runtime.OpenInteraction(ctx, session, vmState, RuntimeStartSpec{
		OperationID: "smoke-cat",
		Kind:        RuntimeOperationCommand,
		AttachStdin: true,
		Command:     &RuntimeCommandSpec{Command: "cat"},
	})
	if err != nil {
		t.Fatalf("OpenInteraction() error = %v", err)
	}
	if frame, err := interaction.Recv(); err != nil || frame.Type != RuntimeOutputStarted {
		t.Fatalf("started frame = %#v, err=%v", frame, err)
	}
	if err := interaction.Send(RuntimeInputFrame{Type: RuntimeInputStdin, Data: []byte("hello attach\n")}); err != nil {
		t.Fatalf("Send(stdin) error = %v", err)
	}
	if err := interaction.CloseSend(); err != nil {
		t.Fatalf("CloseSend() error = %v", err)
	}

	var stdout string
	for {
		frame, err := interaction.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv() error = %v", err)
		}
		switch frame.Type {
		case RuntimeOutputStdout:
			stdout += string(frame.Data)
		case RuntimeOutputResult:
			if frame.Result == nil || !frame.Result.Success || frame.Result.ExitCode != 0 {
				t.Fatalf("result frame = %#v", frame)
			}
		case RuntimeOutputError:
			t.Fatalf("unexpected error frame = %#v", frame)
		}
	}
	result, err := interaction.Wait()
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !result.Success || result.ExitCode != 0 {
		t.Fatalf("Wait() result = %#v", result)
	}
	if stdout != "hello attach\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "hello attach\n")
	}
}
