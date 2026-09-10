package adapters

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type filesystemGuestAgentRuntime struct {
	fakeAgentRuntime
	root           string
	publishes      int
	links          int
	lastProjection driverpkg.GuestSymlinkProjection
	dirWrites      []string
	probeErr       error
	publishErr     error
	linkErr        error
	fileErr        error
}

func (r *filesystemGuestAgentRuntime) guestPath(path string) string {
	return filepath.Join(r.root, strings.TrimPrefix(path, "/"))
}

func (r *filesystemGuestAgentRuntime) Exec(ctx context.Context, _ *domain.Sandbox, _ domain.VMState, spec domain.ExecSpec) (domain.ExecResult, error) {
	if r.probeErr != nil {
		return domain.ExecResult{}, r.probeErr
	}
	if spec.Command != "node" {
		return domain.ExecResult{}, fmt.Errorf("unexpected guest command %q", spec.Command)
	}
	// Execute the actual production Node probe against an isolated guest tree.
	// This is a filesystem integration test, not a live Kubernetes test.
	command := exec.CommandContext(ctx, "node", "-e", spec.Args[1], r.guestPath(spec.Args[2]), r.guestPath(spec.Args[3]))
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if err != nil {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) {
			return domain.ExecResult{}, err
		}
		code = exitError.ExitCode()
	}
	return domain.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: code, Success: code == 0}, nil
}

func (r *filesystemGuestAgentRuntime) WriteGuestFile(_ context.Context, _ *domain.Sandbox, _ domain.VMState, path string, data []byte) error {
	if r.fileErr != nil && strings.HasSuffix(path, "/models.json") {
		return r.fileErr
	}
	path = r.guestPath(path)
	if data == nil {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (r *filesystemGuestAgentRuntime) WriteGuestDir(_ context.Context, _ *domain.Sandbox, _ domain.VMState, source, destination string) error {
	r.dirWrites = append(r.dirWrites, destination)
	return copyGuestTestTree(source, r.guestPath(destination))
}

func (r *filesystemGuestAgentRuntime) PublishGuestDirectory(_ context.Context, _ *domain.Sandbox, _ domain.VMState, publication driverpkg.GuestDirectoryPublication) error {
	source, destination := publication.HostSource, publication.GuestDestination
	if r.publishErr != nil {
		return r.publishErr
	}
	r.publishes++
	destination = r.guestPath(destination)
	control := filepath.Join(filepath.Dir(destination), ".agent-compose-"+filepath.Base(destination))
	generation := filepath.Join(control, fmt.Sprintf("generation-%d", r.publishes))
	if err := copyGuestTestTree(source, generation); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(control, ".owner"), []byte("agent-compose-directory-v1:"+destination), 0o600); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(filepath.Join(filepath.Base(control), filepath.Base(generation)), destination)
}

func (r *filesystemGuestAgentRuntime) EnsureGuestSymlink(_ context.Context, _ *domain.Sandbox, _ domain.VMState, projection driverpkg.GuestSymlinkProjection) error {
	destination, target := projection.GuestPath, projection.RelativeTarget
	r.links++
	r.lastProjection = projection
	if r.linkErr != nil {
		return r.linkErr
	}
	destination = r.guestPath(destination)
	if actual, err := os.Readlink(destination); err == nil && actual == target {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, destination)
}

func copyGuestTestTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}

func TestIntegrationGuestAgentSkillsProbeMatchesCompleteHostContract(t *testing.T) {
	runtime := &filesystemGuestAgentRuntime{root: t.TempDir()}
	root := runtime.guestPath("/root/.agents/skills")
	for name, mode := range map[string]fs.FileMode{
		"alpha/SKILL.md": 0o640, "alpha/run.sh": 0o751,
		"alpha/.agent-compose-skills.json": 0o600, "unicode/𐀀": 0o644,
		"unicode/\uE000": 0o644, "spaces and\nnewline.txt": 0o644,
		".agent-compose-skills.json": 0o600,
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o751); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(name+"\n"), mode); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := execution.AgentSkillsProjectionEntries(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := readGuestAgentSkillsProjection(context.Background(), runtime, nil, domain.VMState{}, "/root/.agents/skills")
	if err != nil || actual.Status != "present" || !slices.Equal(expected, actual.Entries) {
		t.Fatalf("Node/Go complete entry contract mismatch: got=%+v expected=%+v err=%v", actual, expected, err)
	}
	if err := os.Symlink("alpha", filepath.Join(root, "foreign-link")); err != nil {
		t.Fatal(err)
	}
	actual, err = readGuestAgentSkillsProjection(context.Background(), runtime, nil, domain.VMState{}, "/root/.agents/skills")
	if err != nil || actual.Status != "different" {
		t.Fatalf("nested symlink must not be followed: %+v, %v", actual, err)
	}
}

func newGuestSkillsRunner(t *testing.T) (*AgentRunner, *domain.Sandbox, *filesystemGuestAgentRuntime, string) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), GuestHomePath: "/root", GuestWorkspacePath: "/workspace"}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := store.CreateSandbox(context.Background(), "guest skills", "", "k8s", "guest:test", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &filesystemGuestAgentRuntime{root: t.TempDir()}
	runner := NewAgentRunner(AgentRunnerDeps{Config: config, Store: store, Runtimes: fakeRuntimeProvider{runtime: runtime}})
	source := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(source, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "alpha", "SKILL.md"), []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	return runner, sandbox, runtime, source
}

func TestIntegrationGuestSkillsReconciliationChecksGuestStateWithoutRepublishingUnchangedContent(t *testing.T) {
	runner, sandbox, runtime, source := newGuestSkillsRunner(t)
	writer := runner.guestSkillsWriterFor(sandbox)
	if err := writer(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runtime.lastProjection.ManagedMarkers, []string{".agent-compose-skills.json", ".agent-compose-managed"}) {
		t.Fatalf("legacy mirror marker contract = %v", runtime.lastProjection.ManagedMarkers)
	}
	guestFile := runtime.guestPath("/root/.agents/skills/alpha/SKILL.md")
	before, err := os.Stat(guestFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(guestFile)
	if err != nil || !os.SameFile(before, after) || runtime.publishes != 1 {
		t.Fatalf("unchanged guest was republished: publishes=%d error=%v", runtime.publishes, err)
	}
	for _, mutation := range []func() error{
		func() error { return os.WriteFile(guestFile, []byte("guest edit"), 0o640) },
		func() error { return os.Chmod(guestFile, 0o750) },
		func() error { return os.Remove(guestFile) },
		func() error {
			return os.WriteFile(runtime.guestPath("/root/.agents/skills/extra"), []byte("extra"), 0o640)
		},
	} {
		if err := mutation(); err != nil {
			t.Fatal(err)
		}
		previous := runtime.publishes
		if err := writer(context.Background(), source); err != nil {
			t.Fatal(err)
		}
		if runtime.publishes != previous+1 {
			t.Fatal("guest drift was not repaired")
		}
		if data, err := os.ReadFile(guestFile); err != nil || string(data) != "original" {
			t.Fatalf("repair=%q,%v", data, err)
		}
	}
	runtime.root = t.TempDir() // Same sandbox and host projection, fresh Pod filesystem.
	previous := runtime.publishes
	if err := writer(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	if runtime.publishes != previous+1 {
		t.Fatal("new guest did not receive unchanged host content")
	}
}

func TestIntegrationGuestSkillsReconciliationPreservesErrorsAndRetriesMirrorProjection(t *testing.T) {
	runner, sandbox, runtime, source := newGuestSkillsRunner(t)
	writer := runner.guestSkillsWriterFor(sandbox)
	probeErr := errors.New("guest unavailable")
	runtime.probeErr = probeErr
	if err := writer(context.Background(), source); !errors.Is(err, probeErr) || runtime.publishes != 0 {
		t.Fatalf("probe failure = %v", err)
	}
	runtime.probeErr = nil
	linkErr := errors.New("projection unavailable")
	runtime.linkErr = linkErr
	if err := writer(context.Background(), source); !errors.Is(err, linkErr) || runtime.publishes != 0 {
		t.Fatalf("link failure = %v", err)
	}
	runtime.linkErr = nil
	if err := writer(context.Background(), source); err != nil || runtime.publishes != 1 {
		t.Fatalf("mirror retry recopied canonical: %v", err)
	}
}

func TestIntegrationGuestSkillsReconciliationCorrectsGuestAfterHostAcknowledgementFailure(t *testing.T) {
	runner, sandbox, runtime, source := newGuestSkillsRunner(t)
	writer := runner.guestSkillsWriterFor(sandbox)
	skills := []execution.ResolvedAgentSkill{{Name: "alpha", LocalDir: filepath.Join(source, "alpha")}}
	if _, err := execution.WriteAgentSkills(context.Background(), runner.config, sandbox, skills, writer); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(source, "alpha", "SKILL.md")
	if err := os.WriteFile(sourceFile, []byte("unacknowledged new source"), 0o640); err != nil {
		t.Fatal(err)
	}
	acknowledgementErr := errors.New("host lost publication acknowledgement")
	failingWriter := func(ctx context.Context, host string) error {
		if err := writer(ctx, host); err != nil {
			return err
		}
		return acknowledgementErr
	}
	if _, err := execution.WriteAgentSkills(context.Background(), runner.config, sandbox, skills, failingWriter); !errors.Is(err, acknowledgementErr) {
		t.Fatalf("acknowledgement failure = %v", err)
	}
	hostFile := filepath.Join(execution.HostAgentSkillsDir(sandbox), "alpha", "SKILL.md")
	if data, err := os.ReadFile(hostFile); err != nil || string(data) != "original" {
		t.Fatalf("host transaction did not roll back: %q, %v", data, err)
	}
	// The remote write succeeded while the host transaction rolled back. Restore
	// the earlier desired source: host metadata and content now look unchanged,
	// but the real guest must still be checked and repaired.
	if err := os.WriteFile(sourceFile, []byte("original"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.WriteAgentSkills(context.Background(), runner.config, sandbox, skills, writer); err != nil {
		t.Fatal(err)
	}
	if runtime.publishes != 3 {
		t.Fatalf("guest content was not corrected after rollback: %d publications", runtime.publishes)
	}
	actual, err := os.ReadFile(runtime.guestPath("/root/.agents/skills/alpha/SKILL.md"))
	if err != nil || string(actual) != "original" {
		t.Fatalf("guest after host rollback = %q, %v", actual, err)
	}
}

func TestGuestSkillsAndPiFailClosedWhenRuntimeResolutionFails(t *testing.T) {
	runner, sandbox, _, source := newGuestSkillsRunner(t)
	lookupErr := errors.New("runtime registry unavailable")
	runner.runtimes = fakeRuntimeProvider{err: lookupErr}
	if err := runner.guestSkillsWriterFor(sandbox)(context.Background(), source); !errors.Is(err, lookupErr) {
		t.Fatalf("skills lookup error = %v", err)
	}
	if err := runner.syncPiRuntimeConfigToGuest(context.Background(), sandbox, "pi"); !errors.Is(err, lookupErr) {
		t.Fatalf("Pi lookup error = %v", err)
	}
	runner.runtimes = nil
	if writer := runner.guestSkillsWriterFor(sandbox); writer == nil || writer(context.Background(), source) == nil {
		t.Fatal("K8s missing runtime silently skipped skills transfer")
	}
}

func TestIntegrationGuestAgentSkillsProbeRejectsRedirectedPrivateParents(t *testing.T) {
	runtime := &filesystemGuestAgentRuntime{root: t.TempDir()}
	outside := t.TempDir()
	if err := os.MkdirAll(runtime.guestPath("/root"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runtime.guestPath("/root/.agents")); err != nil {
		t.Fatal(err)
	}
	if _, err := readGuestAgentSkillsProjection(context.Background(), runtime, nil, domain.VMState{}, "/root/.agents/skills"); err == nil {
		t.Fatal("redirected parent was read as a private guest tree")
	}
}

func TestGuestSkillsAndPiFailClosedWhenVMStateCannotBeLoaded(t *testing.T) {
	runner, sandbox, runtime, source := newGuestSkillsRunner(t)
	sandbox.Summary.ID = "missing-sandbox-state"
	if err := runner.guestSkillsWriterFor(sandbox)(context.Background(), source); err == nil {
		t.Fatal("skills transfer ignored missing VM state")
	}
	if err := runner.syncPiRuntimeConfigToGuest(context.Background(), sandbox, "pi"); err == nil {
		t.Fatal("Pi transfer ignored missing VM state")
	}
	if runtime.publishes != 0 || runtime.links != 0 {
		t.Fatal("invalid VM state reached guest publication")
	}
}
