package health

import (
	"context"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/internal/config"
	"agent-compose/proto/health/v1/healthv1connect"
)

func TestServiceSnapshotIncludesRuntimeDetails(t *testing.T) {
	testServiceSnapshotIncludesRuntimeDetails(t)
}

func testServiceSnapshotIncludesRuntimeDetails(t *testing.T) {
	t.Helper()
	service := &Service{
		startedAt: time.Now().Add(-2 * time.Second),
		config:    &config.Config{Version: "test-version"},
	}

	status := service.snapshot()
	if status.GetVersion() != "test-version" || status.GetBuildVersion() != "test-version" {
		t.Fatalf("version fields = %q/%q, want test-version", status.GetVersion(), status.GetBuildVersion())
	}
	if status.GetGoVersion() != runtime.Version() {
		t.Fatalf("go version = %q, want %q", status.GetGoVersion(), runtime.Version())
	}
	if status.GetCurrentTime() == "" || status.GetStartedAt() == "" {
		t.Fatalf("expected current and start timestamps, got %#v", status)
	}
	if _, err := time.Parse(time.RFC3339Nano, status.GetCurrentTime()); err != nil {
		t.Fatalf("current time is not RFC3339Nano: %v", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, status.GetStartedAt()); err != nil {
		t.Fatalf("started at is not RFC3339Nano: %v", err)
	}
	if status.GetMemory() == nil {
		t.Fatal("expected memory details")
	}
	if status.GetProcess() == nil {
		t.Fatal("expected process details")
	}
	if status.GetNumGoroutines() == 0 {
		t.Fatal("expected goroutine count")
	}
}

func TestProcessUsageUpdatesProbe(t *testing.T) {
	testProcessUsageUpdatesProbe(t)
}

func testProcessUsageUpdatesProbe(t *testing.T) {
	t.Helper()
	service := &Service{config: &config.Config{}}
	first := service.processUsage(time.Now())
	if first == nil {
		t.Fatal("expected process usage")
	}
	if service.lastProcessProbe.at.IsZero() {
		t.Fatal("expected last process probe to be recorded")
	}

	second := service.processUsage(time.Now().Add(10 * time.Millisecond))
	if second == nil {
		t.Fatal("expected second process usage")
	}
	if second.GetCpuUserMillis()+second.GetCpuSystemMillis() < first.GetCpuUserMillis()+first.GetCpuSystemMillis() {
		t.Fatalf("cpu millis moved backwards: first=%#v second=%#v", first, second)
	}
}

func TestRPCStatusAndSetup(t *testing.T) {
	testRPCStatusAndSetup(t)
}

func testRPCStatusAndSetup(t *testing.T) {
	t.Helper()
	di := do.New()
	app := echo.New()
	do.ProvideValue(di, app)
	do.ProvideValue(di, &config.Config{Version: "setup-version"})

	Setup(di)

	server := httptest.NewServer(app)
	t.Cleanup(server.Close)

	client := healthv1connect.NewHealthServiceClient(server.Client(), server.URL)
	resp, err := client.Status(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if resp.Msg.GetVersion() != "setup-version" {
		t.Fatalf("status version = %q, want setup-version", resp.Msg.GetVersion())
	}
}

func TestRPCWatchStatusSendsInitialSnapshot(t *testing.T) {
	testRPCWatchStatusSendsInitialSnapshot(t)
}

func testRPCWatchStatusSendsInitialSnapshot(t *testing.T) {
	t.Helper()
	service := &Service{
		startedAt: time.Now(),
		config:    &config.Config{Version: "watch-version"},
	}
	app := echo.New()
	path, handler := healthv1connect.NewHealthServiceHandler(&rpcServer{service: service})
	app.Any(path+"*", echo.WrapHandler(handler))

	server := httptest.NewServer(app)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := healthv1connect.NewHealthServiceClient(server.Client(), server.URL)
	stream, err := client.WatchStatus(ctx, connect.NewRequest(&emptypb.Empty{}))
	if err != nil {
		t.Fatalf("WatchStatus returned error: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("expected initial status update, err=%v", stream.Err())
	}
	if got := stream.Msg().GetVersion(); got != "watch-version" {
		t.Fatalf("watch version = %q, want watch-version", got)
	}
	cancel()
}

func TestProcessStatsHelpers(t *testing.T) {
	testProcessStatsHelpers(t)
}

func testProcessStatsHelpers(t *testing.T) {
	t.Helper()
	userMillis, systemMillis := processCPUMillis()
	if userMillis+systemMillis == 0 {
		t.Fatal("expected process CPU usage to be available")
	}
	if processRSSBytes() == 0 {
		t.Fatal("expected process RSS to be available")
	}

	ioStats := readProcessIO()
	if _, err := os.Stat("/proc/self/io"); err == nil {
		// /proc/self/io always reports non-zero rchar for a running process.
		if ioStats.readBytes == 0 && ioStats.writeBytes == 0 && ioStats.readOps == 0 && ioStats.writeOps == 0 {
			t.Fatal("expected process IO stats")
		}
	}
	// Without /proc (e.g. macOS) the rusage fallback legitimately reports zero
	// when all I/O was served from the page cache, so zero is acceptable there.

	rusageStats := processIOFromRusage()
	if rusageStats.readBytes != rusageStats.readOps*512 {
		t.Fatalf("read bytes = %d, read ops = %d", rusageStats.readBytes, rusageStats.readOps)
	}
	if rusageStats.writeBytes != rusageStats.writeOps*512 {
		t.Fatalf("write bytes = %d, write ops = %d", rusageStats.writeBytes, rusageStats.writeOps)
	}
	if maxInt64(-1, 0) != 0 || maxInt64(3, 0) != 3 {
		t.Fatal("maxInt64 did not enforce minimum")
	}
}
