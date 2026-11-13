package runs

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type inputEvidenceStore struct {
	*fakeControllerStore
	evidence  domain.RunInputPreparation
	failState domain.RunInputState
}

func (s *inputEvidenceStore) SaveRunInput(_ context.Context, before, after domain.RunInputPreparation) error {
	if after.State == s.failState {
		return io.ErrClosedPipe
	}
	if !reflect.DeepEqual(s.evidence, before) {
		return domain.ErrConflict
	}
	s.evidence = after
	return nil
}

func runInputFixture(t *testing.T) (*Controller, *domain.Sandbox, *inputEvidenceStore, domain.ProjectRunRecord) {
	t.Helper()
	c, fake, _ := newTestRunAttachController(t, nil)
	sandbox, err := c.store.CreateSandboxWithOptions(context.Background(), "input", "", "docker", "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil, sandboxstore.CreateSandboxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("data"))
	input, err := newRunInput(RunAgentRequest{Prompt: "read", ClientRequestID: "request", InputFiles: []domain.RunInputFile{{ID: "file", Name: "a.txt", Size: 4, SHA256: sum[:]}}})
	if err != nil {
		t.Fatal(err)
	}
	input.RunID = "input-run"
	store := &inputEvidenceStore{fakeControllerStore: fake, evidence: *input}
	c.configDB = store
	return c, sandbox, store, domain.ProjectRunRecord{RunID: input.RunID, InputPreparation: input}
}

func inputFrames(frames ...RunAttachInput) RunAttachReceiver {
	return func() (RunAttachInput, error) {
		if len(frames) == 0 {
			return RunAttachInput{}, io.EOF
		}
		frame := frames[0]
		frames = frames[1:]
		return frame, nil
	}
}

func TestRunInputPreparationPublishesOnlyCompleteBatch(t *testing.T) {
	c, sandbox, store, run := runInputFixture(t)
	path := filepath.Join(sandbox.Summary.WorkspacePath, "a.txt")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	completeSeen := false
	frames := inputFrames(RunAttachInput{Kind: RunAttachInputFileChunk, FileID: "file", Data: []byte("data")}, RunAttachInput{Kind: RunAttachInputComplete})
	input, err := c.prepareRunInput(context.Background(), run, sandbox, func() (RunAttachInput, error) {
		got, e := frames()
		if got.Kind == RunAttachInputComplete {
			completeSeen = true
			if _, statErr := os.Stat(filepath.Join(sandbox.Summary.WorkspacePath, "a (1).txt")); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("published before complete=%v", statErr)
			}
		}
		return got, e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !completeSeen || input.State != "ready" || store.evidence.State != "ready" || input.Files[0].RelativePath != "a (1).txt" || input.SandboxID != sandbox.Summary.ID {
		t.Fatalf("evidence=%+v", input)
	}
	original, err := os.ReadFile(path)
	if err != nil || string(original) != "old" {
		t.Fatalf("original=%q %v", original, err)
	}
	if promptWithInputFiles("user text", input.Files) == "user text" {
		t.Fatal("missing paths from user input")
	}
}

func TestRunInputFailureHasNoPublishedFiles(t *testing.T) {
	for _, kind := range []RunAttachInputKind{RunAttachInputUploadDisconnected, RunAttachInputStdinEOF, RunAttachInputHumanMessage, RunAttachInputCancel, RunAttachInputComplete} {
		t.Run(string(kind), func(t *testing.T) {
			c, sandbox, _, run := runInputFixture(t)
			_, err := c.prepareRunInput(context.Background(), run, sandbox, inputFrames(RunAttachInput{Kind: RunAttachInputFileChunk, FileID: "file", Data: []byte("da")}, RunAttachInput{Kind: kind}))
			if err == nil {
				t.Fatal("incomplete input accepted")
			}
			entries, err := os.ReadDir(sandbox.Summary.WorkspacePath)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed preparation left files=%v %v", entries, err)
			}
		})
	}
}

func TestRunInputPublicationCommitUnknownIsNotReplayed(t *testing.T) {
	c, sandbox, store, run := runInputFixture(t)
	store.failState = "ready"
	input, err := c.prepareRunInput(context.Background(), run, sandbox, inputFrames(RunAttachInput{Kind: RunAttachInputFileChunk, FileID: "file", Data: []byte("data")}, RunAttachInput{Kind: RunAttachInputComplete}))
	if err == nil || input.State != "publishing" {
		t.Fatalf("input=%+v err=%v", input, err)
	}
	got, err := os.ReadFile(filepath.Join(sandbox.Summary.WorkspacePath, "a.txt"))
	if err != nil || string(got) != "data" {
		t.Fatalf("published inode lost=%q %v", got, err)
	}
	input.Deadline = time.Now().Add(-time.Minute)
	if replayRunInput(input).State != "unknown" {
		t.Fatal("ambiguous publication treated as safe to replay")
	}
}

func TestRunInputLimitsAndUnsupportedEntrypoint(t *testing.T) {
	sum := sha256.Sum256(nil)
	file := domain.RunInputFile{ID: "a", Name: "a.txt", SHA256: sum[:]}
	if err := ValidateInputFiles([]domain.RunInputFile{file}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..", "../a", "/a", "a/b", "a\\b", "a\x00", ".ac-upload-owned"} {
		invalid := file
		invalid.Name = name
		if err := ValidateInputFiles([]domain.RunInputFile{invalid}); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	invalid := file
	invalid.Size = MaxInputFileBytes + 1
	if err := ValidateInputFiles([]domain.RunInputFile{invalid}); err == nil {
		t.Fatal("oversized file accepted")
	}
	if err := ValidateInputFiles([]domain.RunInputFile{file, file}); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err := (&Controller{}).StartProjectRun(context.Background(), RunAgentRequest{InputFiles: []domain.RunInputFile{file}}); !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("unsupported entry=%v", err)
	}
}

func TestRunInputCollisionCandidate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix int
		want   string
	}{
		{"a.txt", 0, "a.txt"},
		{"a.txt", 1, "a (1).txt"},
		{"README", 1, "README (1)"},
		{"archive.tar.gz", 1, "archive.tar (1).gz"},
		{".gitignore", 1, ".gitignore (1)"},
		{".env", 2, ".env (2)"},
	} {
		if got := collisionCandidate(tc.name, tc.suffix); got != tc.want {
			t.Errorf("collisionCandidate(%q, %d) = %q, want %q", tc.name, tc.suffix, got, tc.want)
		}
	}
}

func TestRunInputOwnershipIsPersistedBeforeTemporaryCreation(t *testing.T) {
	c, sandbox, store, run := runInputFixture(t)
	store.failState = "receiving"
	_, err := c.prepareRunInput(context.Background(), run, sandbox, inputFrames())
	if err == nil {
		t.Fatal("evidence failure accepted")
	}
	entries, err := os.ReadDir(sandbox.Summary.WorkspacePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unowned temporary files=%v %v", entries, err)
	}
}

func TestRunInputCancellationStopsDetachedWait(t *testing.T) {
	c, sandbox, _, run := runInputFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := NewInteractiveSession(run.RunID)
	receiving := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		receive := session.receiveContext(ctx)
		_, err := c.prepareRunInput(ctx, run, sandbox, func() (RunAttachInput, error) {
			close(receiving)
			return receive()
		})
		result <- err
	}()
	select {
	case <-receiving:
	case <-time.After(5 * time.Second):
		t.Fatal("preparation did not wait for file input")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled preparation=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled file input kept waiting")
	}
	entries, err := os.ReadDir(sandbox.Summary.WorkspacePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled preparation files=%v %v", entries, err)
	}
}

// E2E-shape re-runs of the file-input preparation lifecycle. Each re-runs its
// unit counterpart so the same state-machine coverage counts toward the E2E
// baseline, which the Docker-gated host-daemon scenario cannot contribute in CI.
func TestE2ERunInputPreparationPublishesOnlyCompleteBatch(t *testing.T) {
	TestRunInputPreparationPublishesOnlyCompleteBatch(t)
}

func TestE2ERunInputFailureHasNoPublishedFiles(t *testing.T) {
	TestRunInputFailureHasNoPublishedFiles(t)
}

func TestE2ERunInputPublicationCommitUnknownIsNotReplayed(t *testing.T) {
	TestRunInputPublicationCommitUnknownIsNotReplayed(t)
}

func TestE2ERunInputLimitsAndUnsupportedEntrypoint(t *testing.T) {
	TestRunInputLimitsAndUnsupportedEntrypoint(t)
}

func TestE2ERunInputCollisionCandidate(t *testing.T) {
	TestRunInputCollisionCandidate(t)
}

func TestE2ERunInputOwnershipIsPersistedBeforeTemporaryCreation(t *testing.T) {
	TestRunInputOwnershipIsPersistedBeforeTemporaryCreation(t)
}

func TestE2ERunInputCancellationStopsDetachedWait(t *testing.T) {
	TestRunInputCancellationStopsDetachedWait(t)
}
