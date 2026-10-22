package runs

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// detachProbeTimeout bounds the capability probe exec; a slow guest must not
// stall the run start.
const detachProbeTimeout = 10 * time.Second

// detachCapabilityCache records, per sandbox, whether the guest runtime
// supports detached execution. The answer is a property of the guest image, so
// it is stable for a sandbox's lifetime.
type detachCapabilityCache struct {
	mu    sync.Mutex
	cache map[string]bool
}

func (c *detachCapabilityCache) get(sandboxID string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.cache[sandboxID]
	return value, ok
}

func (c *detachCapabilityCache) set(sandboxID string, value bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil {
		c.cache = map[string]bool{}
	}
	c.cache[sandboxID] = value
}

// detachSupported probes the guest runtime's `capabilities --json` and caches
// the answer per sandbox. Only an answer from the guest is cached: a runtime
// that ran the probe and does not report detach (including one without the
// subcommand, which exits non-zero) stays on the legacy path for the sandbox's
// lifetime, while a probe that never got an answer (timeout, transport error)
// falls back for this run only and is retried next time.
func (c *Controller) detachSupported(ctx context.Context, runtime Runtime, sandbox *domain.Sandbox, vmState domain.VMState) bool {
	if c == nil || c.detachCache == nil || sandbox == nil {
		return false
	}
	sandboxID := sandbox.Summary.ID
	if value, ok := c.detachCache.get(sandboxID); ok {
		return value
	}
	probeCtx, cancel := context.WithTimeout(ctx, detachProbeTimeout)
	defer cancel()
	spec := execution.BuildDetachCapabilitiesSpec(c.config)
	spec.Env = execution.BuildSandboxExecEnv(probeCtx, c.config, sandbox, c.config.GuestHomePath)
	var stdout strings.Builder
	_, err := runtime.ExecStream(probeCtx, sandbox, vmState, spec, func(chunk domain.ExecChunk) {
		if chunk.Stream == domain.StdioStdout {
			stdout.WriteString(chunk.Text)
		}
	})
	if err != nil {
		return false
	}
	caps, ok, _ := execution.ParseDetachCapabilities(stdout.String())
	supported := ok && caps.Detach
	c.detachCache.set(sandboxID, supported)
	return supported
}

// persistDetachedRunFields records the detach decision on the run before the
// detached task starts, so a restarted daemon can re-attach it and re-arm its
// deadline.
func (c *Controller) persistDetachedRunFields(ctx context.Context, run domain.ProjectRunRecord, executionID, kind string, deadline time.Time) error {
	if c.configDB == nil {
		return nil
	}
	// Write onto the stored record, not the caller's snapshot: the run may
	// have moved on since (an agent run records its cell log path when the
	// cell starts), and re-attach needs those fields too.
	if current, err := c.configDB.GetProjectRun(ctx, run.RunID); err == nil {
		run = current
	}
	run.ExecutionID = executionID
	run.Detach = true
	run.DetachKind = kind
	run.Deadline = deadline
	_, err := c.configDB.UpdateProjectRun(ctx, run)
	return err
}

// detachCursorFile returns the sidecar file that records the highest journal
// offset the daemon has consumed for a run. It lives beside the transcript so
// it survives a daemon restart on the same data volume.
func detachCursorFile(artifactsDir string) string {
	return filepath.Join(artifactsDir, "journal-cursor")
}

func readDetachCursor(artifactsDir string) uint64 {
	return ReadDetachCursorFile(detachCursorFile(artifactsDir))
}

// ReadDetachCursorFile returns the journal offset recorded in path, or 0 when
// none was recorded yet.
func ReadDetachCursorFile(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	offset, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	return offset
}

func writeDetachCursor(artifactsDir string, offset uint64) error {
	return WriteDetachCursorFile(detachCursorFile(artifactsDir), offset)
}

// WriteDetachCursorFile atomically records offset in path.
func WriteDetachCursorFile(path string, offset uint64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatUint(offset, 10)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// newDetachExecutionID returns a fresh execution id for a fresh detached start.
//
// A fresh start must never reuse an id already persisted on the run: a retry of
// the same request (Connect replay, SDK caller, a client retry after a restart)
// would otherwise re-enter the same guest execution directory and race a still
// running supervisor for the same journal and state files. Re-attach is the only
// path that reuses a persisted id, and it reads run.ExecutionID directly.
func newDetachExecutionID(_ domain.ProjectRunRecord) string {
	return uuid.NewString()
}
