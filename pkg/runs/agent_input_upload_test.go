package runs

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentinput"
	"github.com/chaitin/agent-compose/pkg/apierror"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runinputs"
)

type uploadProjectStore struct {
	ControllerStore
}

func (uploadProjectStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, error) {
	if id != "project-1" {
		return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", id, "project not found", nil)
	}
	return domain.ProjectRecord{ID: id}, nil
}

const testDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

type fakeUploads map[string]runinputs.Object

func (f fakeUploads) Resolve(projectID, uploadID string) (runinputs.Object, error) {
	object, ok := f[uploadID]
	if !ok || object.ProjectID != projectID {
		return runinputs.Object{}, runinputs.ErrNotFound
	}
	return object, nil
}

func uploadController(uploads UploadResolver) *Controller {
	return &Controller{
		config: &appconfig.Config{
			AgentInputUploadFileLimitBytes: 1 << 20,
			AgentInputUploadTurnLimitBytes: 2 << 20,
		},
		configDB: uploadProjectStore{},
		uploads:  uploads,
	}
}

func uploadRequest(uploadID string) RunAgentRequest {
	return RunAgentRequest{
		ProjectID: "project-1", AgentName: "worker", Prompt: "read it",
		Input: []agentinput.Part{agentinput.FilePart(agentinput.File{Name: "big.csv", Source: agentinput.SourceUpload, UploadID: uploadID})},
	}
}

func TestAdmitAgentInputResolvesUploads(t *testing.T) {
	uploads := fakeUploads{"up-1": {UploadID: "up-1", ProjectID: "project-1", SHA256: testDigest, SizeBytes: 512 << 10, Path: "/staged/up-1"}}
	parts, _, err := uploadController(uploads).admitAgentInput(context.Background(), uploadRequest("up-1"))
	if err != nil {
		t.Fatalf("admitAgentInput: %v", err)
	}
	file := parts[1].File
	if file.UploadPath != "/staged/up-1" || file.UploadSize != 512<<10 || file.SHA256 != testDigest {
		t.Fatalf("resolved file = %+v", file)
	}
}

func TestAdmitAgentInputRejectsMissingUploadAsNotFound(t *testing.T) {
	uploads := fakeUploads{"up-1": {UploadID: "up-1", ProjectID: "project-2", SizeBytes: 1, Path: "/staged/up-1"}}
	_, _, err := uploadController(uploads).admitAgentInput(context.Background(), uploadRequest("up-1"))
	if !errors.Is(err, domain.ErrNotFound) || errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want NOT_FOUND without the invalid-request sentinel", err)
	}
	if !apierror.IsNotApplied(err) {
		t.Fatalf("err = %v, want NOT_APPLIED: admission runs before the run exists", err)
	}
}

func TestAdmitAgentInputRejectsUploadsWithoutStagingArea(t *testing.T) {
	_, _, err := uploadController(nil).admitAgentInput(context.Background(), uploadRequest("up-1"))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want an invalid request", err)
	}
}

func TestAdmitAgentInputAppliesUploadFileLimit(t *testing.T) {
	uploads := fakeUploads{"up-1": {UploadID: "up-1", ProjectID: "project-1", SizeBytes: 2 << 20, Path: "/staged/up-1"}}
	_, _, err := uploadController(uploads).admitAgentInput(context.Background(), uploadRequest("up-1"))
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want the upload file limit to reject it", err)
	}
}

// startRunStore records run persistence so a test can prove admission fails
// before any run record is written. BeginRun reaches CreateProjectRunWithEvents
// only after admission, so a rejected upload must leave created empty.
type startRunStore struct {
	ControllerStore
	created   map[string]domain.ProjectRunRecord
	agent     domain.ProjectAgentRecord
	existing  map[string]domain.ProjectRunRecord
	getRunErr error
}

func (s *startRunStore) GetProjectAgent(_ context.Context, projectID, name string) (domain.ProjectAgentRecord, error) {
	if s.agent.ProjectID != projectID || s.agent.AgentName != name {
		return domain.ProjectAgentRecord{}, domain.ResourceError(domain.ErrNotFound, "project agent", name, "project agent not found", nil)
	}
	return s.agent, nil
}

func (s *startRunStore) GetProjectRun(_ context.Context, runID string) (domain.ProjectRunRecord, error) {
	if s.getRunErr != nil {
		return domain.ProjectRunRecord{}, s.getRunErr
	}
	if run, ok := s.existing[runID]; ok {
		return run, nil
	}
	return domain.ProjectRunRecord{}, sql.ErrNoRows
}

func (s *startRunStore) GetProject(_ context.Context, id string) (domain.ProjectRecord, error) {
	if id != "project-1" {
		return domain.ProjectRecord{}, domain.ResourceError(domain.ErrNotFound, "project", id, "project not found", nil)
	}
	return domain.ProjectRecord{ID: id}, nil
}

func (s *startRunStore) CreateProjectRunWithEvents(_ context.Context, run domain.ProjectRunRecord, _ []domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	if s.created == nil {
		s.created = map[string]domain.ProjectRunRecord{}
	}
	s.created[run.RunID] = run
	return run, nil
}

func TestStartProjectRunRetryReturnsExistingRunWhenUploadExpired(t *testing.T) {
	runID, err := projects.StableProjectRunID("project-1", "worker", string(NormalizeSource("")), "retry-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{domain.ProjectRunStatusSucceeded, domain.ProjectRunStatusPending} {
		t.Run(status, func(t *testing.T) {
			store := &startRunStore{
				agent: domain.ProjectAgentRecord{ProjectID: "project-1", AgentName: "worker"},
				existing: map[string]domain.ProjectRunRecord{
					runID: {RunID: runID, ProjectID: "project-1", AgentName: "worker", Status: status},
				},
			}
			controller := &Controller{
				config: &appconfig.Config{
					AgentInputUploadFileLimitBytes: 1 << 20,
					AgentInputUploadTurnLimitBytes: 2 << 20,
				},
				configDB: store,
				// The upload the first attempt referenced has since expired.
				uploads: fakeUploads{},
			}
			req := uploadRequest("up-expired")
			req.ClientRequestID = "retry-1"
			started, err := controller.StartProjectRun(context.Background(), req)
			if err != nil {
				t.Fatalf("idempotent retry: %v", err)
			}
			if started.Run.RunID != runID {
				t.Fatalf("run = %s, want the existing %s", started.Run.RunID, runID)
			}
			if len(store.created) != 0 {
				t.Fatalf("retry created runs = %v, want none", store.created)
			}
		})
	}
}

func TestStartProjectRunSurfacesLookupFailure(t *testing.T) {
	lookupErr := errors.New("config store is unavailable")
	store := &startRunStore{
		agent:     domain.ProjectAgentRecord{ProjectID: "project-1", AgentName: "worker"},
		getRunErr: lookupErr,
	}
	controller := &Controller{
		config: &appconfig.Config{
			AgentInputUploadFileLimitBytes: 1 << 20,
			AgentInputUploadTurnLimitBytes: 2 << 20,
		},
		configDB: store,
		uploads:  fakeUploads{},
	}
	req := uploadRequest("up-1")
	req.ClientRequestID = "retry-1"
	_, err := controller.StartProjectRun(context.Background(), req)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want the lookup failure to surface", err)
	}
	if apierror.IsNotApplied(err) {
		t.Fatalf("err = %v, want no NOT_APPLIED retry hint when the lookup failed", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("created runs = %v, want none", store.created)
	}
}

func TestStartProjectRunRetryReturnsExistingRunWithoutUploads(t *testing.T) {
	runID, err := projects.StableProjectRunID("project-1", "worker", string(NormalizeSource("")), "retry-text")
	if err != nil {
		t.Fatal(err)
	}
	store := &startRunStore{
		agent: domain.ProjectAgentRecord{ProjectID: "project-1", AgentName: "worker"},
		existing: map[string]domain.ProjectRunRecord{
			runID: {RunID: runID, ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusSucceeded},
		},
	}
	controller := &Controller{config: &appconfig.Config{}, configDB: store}
	req := RunAgentRequest{ProjectID: "project-1", AgentName: "worker", Prompt: "hello", ClientRequestID: "retry-text"}
	started, err := controller.StartProjectRun(context.Background(), req)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if started.Run.RunID != runID {
		t.Fatalf("run = %s, want the existing %s", started.Run.RunID, runID)
	}
	if len(store.created) != 0 {
		t.Fatalf("retry created runs = %v, want none", store.created)
	}
}

// TestStartProjectRunRejectsUnresolvableUploadBeforePersisting pins the run
// entry path: a missing or cross-project upload fails admission with NOT_FOUND
// and NOT_APPLIED before BeginRun, so no run record is persisted. An expired
// upload surfaces as the same ErrNotFound from Resolve (the object is already
// gone), so it shares this path; store-level expiry is covered in pkg/runinputs.
func TestStartProjectRunRejectsUnresolvableUploadBeforePersisting(t *testing.T) {
	cases := map[string]UploadResolver{
		"missing upload":       fakeUploads{},
		"other-project upload": fakeUploads{"up-1": {UploadID: "up-1", ProjectID: "project-2", SizeBytes: 1, Path: "/staged/up-1"}},
	}
	for name, uploads := range cases {
		t.Run(name, func(t *testing.T) {
			store := &startRunStore{}
			controller := &Controller{
				config: &appconfig.Config{
					AgentInputUploadFileLimitBytes: 1 << 20,
					AgentInputUploadTurnLimitBytes: 2 << 20,
				},
				configDB: store,
				uploads:  uploads,
			}
			_, err := controller.StartProjectRun(context.Background(), uploadRequest("up-1"))
			if !errors.Is(err, domain.ErrNotFound) || errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want NOT_FOUND without the invalid-request sentinel", err)
			}
			if !apierror.IsNotApplied(err) {
				t.Fatalf("err = %v, want NOT_APPLIED: admission runs before the run exists", err)
			}
			if len(store.created) != 0 {
				t.Fatalf("created runs = %v, want none persisted", store.created)
			}
		})
	}
}
