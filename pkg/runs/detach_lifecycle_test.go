package runs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// detachRecordingRuntime records every guest subcommand it is asked to run and
// replays records as the attach output.
type detachRecordingRuntime struct {
	mu       sync.Mutex
	commands []string
	specs    []domain.ExecSpec
	records  []string
	// attachBlocks keeps attach open until its context ends.
	attachBlocks bool
	probeErr     error
	probeOutput  string
	probeCalls   int
}

func (r *detachRecordingRuntime) ExecStream(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	name := spec.Command
	if len(spec.Args) > 0 {
		name = spec.Args[0]
	}
	if spec.Command == "sh" {
		name = "start"
	}
	r.mu.Lock()
	r.commands = append(r.commands, name)
	r.specs = append(r.specs, spec)
	if name == "capabilities" {
		r.probeCalls++
	}
	r.mu.Unlock()
	switch name {
	case "capabilities":
		if r.probeErr != nil {
			return domain.ExecResult{}, r.probeErr
		}
		writer(domain.ExecChunk{Stream: domain.StdioStdout, Text: r.probeOutput})
		return domain.ExecResult{}, nil
	case "attach":
		if r.attachBlocks {
			<-ctx.Done()
			return domain.ExecResult{}, ctx.Err()
		}
		for _, record := range r.records {
			writer(domain.ExecChunk{Stream: domain.StdioStdout, Text: record + "\n"})
		}
	}
	return domain.ExecResult{}, nil
}

func (r *detachRecordingRuntime) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.commands...)
}

func detachTestOptions(rt Runtime) DetachedTask {
	return DetachedTask{
		Config:      &config.Config{},
		Runtime:     rt,
		Sandbox:     &domain.Sandbox{},
		ExecutionID: "exec-1",
	}
}

func TestFollowDetachedCommandCleansUpAfterExit(t *testing.T) {
	rt := &detachRecordingRuntime{records: []string{`{"t":"exit","code":0,"reason":"completed"}`}}
	result := followDetachedCommand(context.Background(), detachTestOptions(rt))
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if got := strings.Join(rt.seen(), ","); got != "attach,cleanup" {
		t.Fatalf("commands = %s, want attach,cleanup", got)
	}
}

func TestFollowDetachedCommandKeepsStateOnDisconnect(t *testing.T) {
	shutdown := make(chan struct{})
	execCtx, cancel := runExecContext(WithDaemonShutdown(context.Background(), shutdown))
	defer cancel()
	rt := &detachRecordingRuntime{attachBlocks: true}

	done := make(chan detachedExecutionResult, 1)
	go func() { done <- followDetachedCommand(execCtx, detachTestOptions(rt)) }()
	close(shutdown)

	if result := <-done; !errors.Is(result.Err, ErrRunDisconnected) {
		t.Fatalf("err = %v, want ErrRunDisconnected", result.Err)
	}
	for _, name := range rt.seen() {
		if name == "cleanup" {
			t.Fatal("cleanup ran after a disconnect; the next daemon could not resume")
		}
	}
}

func TestStartDetachedCommandHandsRemainingTimeoutToSupervisor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	rt := &detachRecordingRuntime{}
	opts := detachTestOptions(rt)
	opts.TaskTimeout = remainingTimeout(ctx)
	opts.TaskArgs = []string{"exec"}
	if result := startDetachedCommand(ctx, opts); result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	joined := strings.Join(rt.specs[0].Args, " ")
	if !strings.Contains(joined, "'--task-timeout-ms' '35") {
		t.Fatalf("start args %q missing the remaining timeout (~3600000ms)", joined)
	}
	if remainingTimeout(context.Background()) != 0 {
		t.Fatal("remainingTimeout without a deadline must be 0")
	}
}

func TestDetachFollowerPersistsCursorOncePerChunk(t *testing.T) {
	var persisted []uint64
	var forwardedAtPersist []int
	forwarded := 0
	f := newDetachFollower(
		func(domain.ExecChunk) { forwarded++ },
		func(offset uint64) error {
			persisted = append(persisted, offset)
			forwardedAtPersist = append(forwardedAtPersist, forwarded)
			return nil
		},
	)
	f.consume(domain.ExecChunk{Stream: domain.StdioStdout, Text: `{"t":"out","s":"stdout","text":"a","offset":0}` + "\n" +
		`{"t":"out","s":"stdout","text":"b","offset":1}` + "\n" +
		`{"t":"gap","from":2,"to":10}` + "\n"})
	if len(persisted) != 1 || persisted[0] != 10 {
		t.Fatalf("persisted = %v, want one write of 10", persisted)
	}
	if forwardedAtPersist[0] != 0 || forwarded != 3 {
		t.Fatalf("cursor must be persisted before forwarding: forwarded at persist %v, total %d", forwardedAtPersist, forwarded)
	}
	// A chunk that only completes no record does not write the cursor.
	f.consume(domain.ExecChunk{Stream: domain.StdioStdout, Text: `{"t":"out"`})
	if len(persisted) != 1 {
		t.Fatalf("persisted = %v after a partial chunk, want unchanged", persisted)
	}
}

func TestDetachSupportedRetriesAfterProbeError(t *testing.T) {
	controller := &Controller{config: &config.Config{}, detachCache: &detachCapabilityCache{}}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sb-1"}}
	rt := &detachRecordingRuntime{probeErr: errors.New("exec transport closed")}

	if controller.detachSupported(context.Background(), rt, sandbox, domain.VMState{}) {
		t.Fatal("failed probe reported detach support")
	}
	rt.probeErr = nil
	rt.probeOutput = `{"detach":true}` + "\n"
	if !controller.detachSupported(context.Background(), rt, sandbox, domain.VMState{}) {
		t.Fatal("probe after a transient failure did not pick up detach support")
	}
	// Now cached: no further probes.
	controller.detachSupported(context.Background(), rt, sandbox, domain.VMState{})
	if rt.probeCalls != 2 {
		t.Fatalf("probe calls = %d, want 2", rt.probeCalls)
	}
}

func TestDetachSupportedCachesLegacyRuntimeAnswer(t *testing.T) {
	controller := &Controller{config: &config.Config{}, detachCache: &detachCapabilityCache{}}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sb-1"}}
	// An old runtime without the subcommand answers with no JSON.
	rt := &detachRecordingRuntime{probeOutput: "error: unknown command 'capabilities'\n"}
	for range 2 {
		if controller.detachSupported(context.Background(), rt, sandbox, domain.VMState{}) {
			t.Fatal("legacy runtime reported detach support")
		}
	}
	if rt.probeCalls != 1 {
		t.Fatalf("probe calls = %d, want 1 (legacy answer cached)", rt.probeCalls)
	}
}
