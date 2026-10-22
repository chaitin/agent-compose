package runs

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// fakeAttachInteraction plays the `attach --stdin` exec: tests push attach
// output lines, inspect the stdin bytes it received, and it ends when its
// context is cancelled (as the driver kills it) or the output is closed.
type fakeAttachInteraction struct {
	ctx    context.Context
	output chan driverpkg.RuntimeOutputFrame
	mu     sync.Mutex
	stdin  []byte
	closed bool
}

func (i *fakeAttachInteraction) Send(frame driverpkg.RuntimeInputFrame) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stdin = append(i.stdin, frame.Data...)
	return nil
}

func (i *fakeAttachInteraction) CloseSend() error {
	i.mu.Lock()
	i.closed = true
	i.mu.Unlock()
	return nil
}

func (i *fakeAttachInteraction) Recv() (driverpkg.RuntimeOutputFrame, error) {
	select {
	case frame, ok := <-i.output:
		if !ok {
			return driverpkg.RuntimeOutputFrame{}, io.EOF
		}
		return frame, nil
	case <-i.ctx.Done():
		return driverpkg.RuntimeOutputFrame{}, i.ctx.Err()
	}
}

func (i *fakeAttachInteraction) Wait() (driverpkg.RuntimeResult, error) {
	return driverpkg.RuntimeResult{Success: true}, nil
}

func (i *fakeAttachInteraction) stdinFrames(t *testing.T) []string {
	t.Helper()
	i.mu.Lock()
	data := append([]byte(nil), i.stdin...)
	i.mu.Unlock()
	var frames []string
	for len(data) >= 5 {
		length := int(binary.BigEndian.Uint32(data[1:5]))
		frames = append(frames, string(data[0])+":"+string(data[5:5+length]))
		data = data[5+length:]
	}
	return frames
}

type fakeDetachInteractionRuntime struct {
	detachRecordingRuntime
	mu          sync.Mutex
	interaction *fakeAttachInteraction
	openSpec    driverpkg.RuntimeStartSpec
}

func (r *fakeDetachInteractionRuntime) OpenInteraction(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, spec driverpkg.RuntimeStartSpec) (driverpkg.RuntimeInteraction, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.openSpec = spec
	r.interaction = &fakeAttachInteraction{ctx: ctx, output: make(chan driverpkg.RuntimeOutputFrame, 16)}
	return r.interaction, nil
}

func (r *fakeDetachInteractionRuntime) attach() *fakeAttachInteraction {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.interaction
}

func detachInteractionTask(rt Runtime, persisted *[]uint64) DetachedTask {
	cfg := &config.Config{}
	config.ApplyDefaultGuestPaths(cfg)
	return DetachedTask{
		Config: cfg, Runtime: rt, Sandbox: &domain.Sandbox{}, ExecutionID: "exec-1", Cursor: 0,
		PersistCursor: func(offset uint64) error { *persisted = append(*persisted, offset); return nil },
	}
}

func stdoutFrame(line string) driverpkg.RuntimeOutputFrame {
	return driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStdout, Data: []byte(line + "\n")}
}

func TestDetachedInteractionTranslatesAttachRecords(t *testing.T) {
	rt := &fakeDetachInteractionRuntime{}
	var persisted []uint64
	interaction, err := startDetachedInteraction(context.Background(), detachedInteractionOptions{
		Task: detachInteractionTask(rt, &persisted), Interaction: rt, OperationID: "run-1", Origin: "test",
	})
	if err != nil {
		t.Fatalf("startDetachedInteraction: %v", err)
	}
	start := strings.Join(rt.specs[0].Args, " ")
	if !strings.Contains(start, "'--stdin'") {
		t.Fatalf("start args %q missing --stdin", start)
	}
	if args := strings.Join(rt.openSpec.Command.Args, " "); !strings.Contains(args, "attach --execution-id exec-1 --from 0") || !strings.Contains(args, "--stdin") || !rt.openSpec.AttachStdin {
		t.Fatalf("attach spec = %+v", rt.openSpec)
	}
	attach := rt.attach()
	attach.output <- stdoutFrame(`{"t":"out","s":"stdout","text":"hello\n","offset":0}`)
	attach.output <- driverpkg.RuntimeOutputFrame{Type: driverpkg.RuntimeOutputStderr, Data: []byte("node warning\n")}
	attach.output <- stdoutFrame(`{"t":"out","s":"stderr","text":"oops\n","offset":6}`)
	attach.output <- stdoutFrame(`{"t":"exit","code":3,"reason":"completed"}`)

	var frames []driverpkg.RuntimeOutputFrame
	for {
		frame, err := interaction.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		frames = append(frames, frame)
	}
	if len(frames) != 3 || string(frames[0].Data) != "hello\n" || frames[1].Type != driverpkg.RuntimeOutputStderr || string(frames[1].Data) != "oops\n" {
		t.Fatalf("frames = %+v; the attach process's own stderr must not reach the run", frames)
	}
	if frames[2].Type != driverpkg.RuntimeOutputResult || frames[2].Result.ExitCode != 3 || frames[2].Result.Success {
		t.Fatalf("result frame = %+v", frames[2])
	}
	if result, err := interaction.Wait(); err != nil || result.ExitCode != 3 {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
	if len(persisted) == 0 || persisted[len(persisted)-1] != 11 {
		t.Fatalf("persisted cursor = %v, want last 11", persisted)
	}
	if seen := rt.seen(); seen[len(seen)-1] != "cleanup" {
		t.Fatalf("guest execs = %v, want cleanup after the exit", seen)
	}
}

func TestDetachedInteractionFramesInputAndEndsItOnCloseSend(t *testing.T) {
	rt := &fakeDetachInteractionRuntime{}
	var persisted []uint64
	interaction, err := openDetachedInteraction(context.Background(), detachedInteractionOptions{Task: detachInteractionTask(rt, &persisted), Interaction: rt})
	if err != nil {
		t.Fatalf("openDetachedInteraction: %v", err)
	}
	_ = interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: []byte("hi\n")})
	_ = interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputSignal, Signal: "SIGINT"})
	_ = interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputResize, Rows: 10, Cols: 10})
	_ = interaction.CloseSend()
	if got := strings.Join(rt.attach().stdinFrames(t), "|"); got != "D:hi\n|S:SIGINT|E:" {
		t.Fatalf("stdin frames = %q", got)
	}
}

func TestDetachedInteractionOnlyDisconnectsOnShutdown(t *testing.T) {
	rt := &fakeDetachInteractionRuntime{}
	shutdown := make(chan struct{})
	var persisted []uint64
	interaction, err := openDetachedInteraction(WithDaemonShutdown(context.Background(), shutdown), detachedInteractionOptions{Task: detachInteractionTask(rt, &persisted), Interaction: rt})
	if err != nil {
		t.Fatalf("openDetachedInteraction: %v", err)
	}
	_ = interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: []byte("hi\n")})
	close(shutdown)
	if _, err := interaction.Recv(); !errors.Is(err, ErrRunDisconnected) {
		t.Fatalf("Recv err = %v, want ErrRunDisconnected", err)
	}
	// An input pump ending the session while the run is torn down must not
	// reach the task.
	if err := interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: []byte(`{"type":"eof"}`)}); !errors.Is(err, ErrRunDisconnected) {
		t.Fatalf("Send after shutdown err = %v, want ErrRunDisconnected", err)
	}
	_ = interaction.CloseSend()
	if got := strings.Join(rt.attach().stdinFrames(t), "|"); got != "D:hi\n" {
		t.Fatalf("stdin frames = %q; a shutdown must not end the task's input", got)
	}
	for _, name := range rt.seen() {
		if name == "stop" || name == "cleanup" {
			t.Fatalf("guest got %q on shutdown", name)
		}
	}
}

func TestDetachedInteractionStopsTaskOnUserStop(t *testing.T) {
	rt := &fakeDetachInteractionRuntime{}
	ctx, cancel := context.WithCancel(context.Background())
	var persisted []uint64
	interaction, err := openDetachedInteraction(ctx, detachedInteractionOptions{Task: detachInteractionTask(rt, &persisted), Interaction: rt})
	if err != nil {
		t.Fatalf("openDetachedInteraction: %v", err)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(rt.seen(), ","), "stop") {
		if time.Now().After(deadline) {
			t.Fatalf("stop never reached the supervisor: %v", rt.seen())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The supervisor stops the task and attach then delivers its exit.
	rt.attach().output <- stdoutFrame(`{"t":"exit","code":143,"reason":"stopped","stopReason":"user"}`)
	frame, err := interaction.Recv()
	if err != nil || frame.Type != driverpkg.RuntimeOutputResult || frame.Result.Success || !strings.Contains(frame.Result.Error, "stopped by user") {
		t.Fatalf("frame = %+v, err = %v", frame, err)
	}
}

func TestReattachInteractiveCommandRunServesNewClients(t *testing.T) {
	controller, run := newReattachTestController(t, nil)
	rt := &fakeDetachInteractionRuntime{}
	controller.runtime = func(*domain.Sandbox) (Runtime, error) { return rt, nil }
	controller.interactiveSessions = NewInteractiveSessionManager()
	run.DetachKind = domain.DetachKindInteractiveCommand
	sandbox, err := controller.store.GetSandbox(context.Background(), run.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if err := writeInteractiveCommandText(projectRunCommandArtifactsDir(run, sandbox), "cat"); err != nil {
		t.Fatal(err)
	}

	done := make(chan domain.ProjectRunRecord, 1)
	go func() {
		finished, err := controller.ReattachDetachedRun(context.Background(), run)
		if err != nil {
			t.Errorf("ReattachDetachedRun: %v", err)
		}
		done <- finished
	}()
	var session *InteractiveSession
	deadline := time.Now().Add(5 * time.Second)
	for {
		if session, err = controller.interactiveSessions.Get(run.RunID); err == nil && rt.attach() != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("interactive session was not recreated")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if session.State() != InteractiveSessionDetached {
		t.Fatalf("session state = %q, want detached until a client attaches", session.State())
	}
	attachment, err := controller.interactiveSessions.Attach(run.RunID)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	defer attachment.Close()
	outputs, unsubscribe, err := session.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	if err := attachment.Send(context.Background(), RunAttachInput{Kind: RunAttachInputStdin, Data: []byte("x\n")}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	attach := rt.attach()
	attach.output <- stdoutFrame(`{"t":"out","s":"stdout","text":"x\n","offset":0}`)
	select {
	case output := <-outputs:
		if string(output.Data) != "x\n" {
			t.Fatalf("client output = %+v", output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client got no output")
	}
	// The reattached task forwards the client's stdin asynchronously. Wait for
	// it to arrive before ending the run so the assertion below does not race
	// the input pump being torn down on exit.
	deadline = time.Now().Add(5 * time.Second)
	for {
		if frames := attach.stdinFrames(t); len(frames) > 0 && frames[0] == "D:x\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("client stdin never reached the reattached task: %q", attach.stdinFrames(t))
		}
		time.Sleep(10 * time.Millisecond)
	}
	attach.output <- stdoutFrame(`{"t":"exit","code":0,"reason":"completed"}`)

	finished := <-done
	if finished.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", finished.Status)
	}
	if frames := attach.stdinFrames(t); len(frames) == 0 || frames[0] != "D:x\n" {
		t.Fatalf("stdin frames = %q, want the client's input", frames)
	}
	if _, err := controller.interactiveSessions.Get(run.RunID); !errors.Is(err, ErrInteractiveSessionNotFound) {
		t.Fatalf("session after the run ended: err = %v, want not found", err)
	}
}

func TestInteractiveRunDetachOnlyForClientlessNonTTYRuns(t *testing.T) {
	controller, run := newReattachTestController(t, nil)
	controller.detachCache = &detachCapabilityCache{}
	controller.runtime = func(*domain.Sandbox) (Runtime, error) {
		rt := &fakeDetachInteractionRuntime{}
		rt.probeOutput = `{"detach":true}` + "\n"
		return rt, nil
	}
	sandbox, _ := controller.store.GetSandbox(context.Background(), run.SandboxID)
	runCtx := interactionRunContext{Run: run, Sandbox: sandbox}
	cases := []struct {
		name  string
		start RunAttachInput
		mode  RunAttachMode
		want  string
	}{
		{"clientless prompt", RunAttachInput{DisconnectPolicy: AttachDisconnectDetach}, RunAttachModePrompt, domain.DetachKindInteractivePrompt},
		{"clientless command", RunAttachInput{DisconnectPolicy: AttachDisconnectDetach}, RunAttachModeCommand, domain.DetachKindInteractiveCommand},
		{"tty", RunAttachInput{DisconnectPolicy: AttachDisconnectDetach, TTY: true}, RunAttachModeCommand, ""},
		{"client bound", RunAttachInput{}, RunAttachModePrompt, ""},
	}
	for _, tc := range cases {
		got := controller.interactiveRunDetach(context.Background(), runCtx, tc.start, tc.mode)
		kind := ""
		if got != nil {
			kind = got.Kind
		}
		if kind != tc.want {
			t.Fatalf("%s: kind = %q, want %q", tc.name, kind, tc.want)
		}
	}
}
