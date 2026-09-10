package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// syncPiRuntimeConfigToGuest publishes the model catalog generated for this
// invocation. Shared-mount runtimes already see the host write; a reused Pod
// needs the updated file even though its private home is not reseeded.
func (r *AgentRunner) syncPiRuntimeConfigToGuest(ctx context.Context, session *domain.Sandbox, provider string) error {
	if domain.NormalizeAgentKind(provider) != "pi" {
		return nil
	}
	if r == nil || r.runtimes == nil || r.store == nil {
		return fmt.Errorf("pi guest runtime dependencies are unavailable")
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return fmt.Errorf("resolve Pi guest runtime: %w", err)
	}
	guestWriter, ok := runtime.(GuestFileWriter)
	if !ok {
		if session != nil && session.Summary.Driver == "k8s" {
			return fmt.Errorf("runtime does not support Pi guest file transfer")
		}
		return nil
	}
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return fmt.Errorf("load Pi guest runtime state: %w", err)
	}
	writer := func(ctx context.Context, guestPath string, content []byte) error {
		return guestWriter.WriteGuestFile(ctx, session, vmState, guestPath, content)
	}
	hostPath := filepath.Join(execution.HostSandboxHome(session), ".pi", "agent", "models.json")
	if _, err := os.Stat(hostPath); errors.Is(err, os.ErrNotExist) {
		// A daemon without a managed model catalog leaves provider discovery
		// to the guest, just as it does for a mounted home.
		return nil
	} else if err != nil {
		return fmt.Errorf("inspect Pi runtime config: %w", err)
	}
	guestPath := filepath.Join(llms.GuestPiAgentDir(r.config), "models.json")
	if err := execution.SyncHostFileToGuest(ctx, hostPath, guestPath, writer); err != nil {
		return fmt.Errorf("push Pi runtime config to guest: %w", err)
	}
	return nil
}

// guestFileReaderFor returns a pull function for runtimes without a shared
// filesystem. A nil result tells callers to use the existing mounted path.
func (r *AgentRunner) guestFileReaderFor(session *domain.Sandbox) execution.GuestFileReaderFunc {
	if r == nil || r.runtimes == nil || r.store == nil {
		return nil
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return nil
	}
	reader, ok := runtime.(GuestFileReader)
	if !ok {
		return nil
	}
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil
	}
	return func(ctx context.Context, guestPath string) ([]byte, error) {
		return reader.ReadGuestFile(ctx, session, vmState, guestPath)
	}
}

func (r *AgentRunner) guestFileWriterFor(session *domain.Sandbox) execution.GuestFileWriterFunc {
	if r == nil || r.runtimes == nil || r.store == nil {
		return nil
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return nil
	}
	writer, ok := runtime.(GuestFileWriter)
	if !ok {
		return nil
	}
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil
	}
	return func(ctx context.Context, guestPath string, content []byte) error {
		return writer.WriteGuestFile(ctx, session, vmState, guestPath, content)
	}
}

func (r *AgentRunner) guestDirWriterFor(session *domain.Sandbox) execution.GuestDirWriterFunc {
	if r == nil || r.runtimes == nil || r.store == nil {
		return nil
	}
	runtime, err := r.runtimes.ForSession(session)
	if err != nil {
		return nil
	}
	writer, ok := runtime.(GuestDirWriter)
	if !ok {
		return nil
	}
	vmState, err := r.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil
	}
	return func(ctx context.Context, hostSrcDir, guestDir string) error {
		return writer.WriteGuestDir(ctx, session, vmState, hostSrcDir, guestDir)
	}
}

// syncSandboxGuestDirectories transfers the daemon-owned workspace and home
// snapshots on a fresh start, before per-run skills and runtime files. Drivers with
// shared mounts do not expose GuestDirWriter, so they keep their existing path.
func (r *AgentRunner) syncSandboxGuestDirectories(ctx context.Context, session *domain.Sandbox) error {
	writeGuestDir := r.guestDirWriterFor(session)
	if writeGuestDir == nil {
		return nil
	}
	directories := []struct {
		name      string
		hostPath  string
		guestPath string
	}{
		{name: "workspace", hostPath: session.Summary.WorkspacePath, guestPath: r.config.GuestWorkspacePath},
		{name: "home", hostPath: execution.HostSandboxHome(session), guestPath: r.config.GuestHomePath},
	}
	for _, directory := range directories {
		info, err := os.Stat(directory.hostPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect sandbox %s directory %s: %w", directory.name, directory.hostPath, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("sandbox %s path %s is not a directory", directory.name, directory.hostPath)
		}
		if strings.TrimSpace(directory.guestPath) == "" {
			return fmt.Errorf("guest %s path is required", directory.name)
		}
		if err := writeGuestDir(ctx, filepath.Clean(directory.hostPath), filepath.Clean(directory.guestPath)); err != nil {
			return fmt.Errorf("push sandbox %s to guest: %w", directory.name, err)
		}
	}
	return nil
}
