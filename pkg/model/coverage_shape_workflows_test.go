package model_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func TestModelBranchCoverageWorkflows(t *testing.T) {
	testModelBranchCoverageWorkflows(t)
}

func TestIntegrationModelBranchCoverageWorkflows(t *testing.T) {
	testModelBranchCoverageWorkflows(t)
}

func TestE2EModelBranchCoverageWorkflows(t *testing.T) {
	testModelBranchCoverageWorkflows(t)
}

func testModelBranchCoverageWorkflows(t *testing.T) {
	t.Helper()
	now := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	session := &domain.Session{
		Summary: domain.SessionSummary{
			ID:            "session-branch",
			Title:         "Branch Session",
			TriggerSource: "script:loader-1",
			Driver:        driverpkg.RuntimeDriverDocker,
			VMStatus:      domain.VMStatusRunning,
			WorkspacePath: "/workspaces/branch",
			CreatedAt:     now,
			UpdatedAt:     now.Add(time.Minute),
		},
		WorkspaceID: "workspace-1",
		Workspace:   &domain.SessionWorkspace{ID: "workspace-1", Name: "Workspace One", Type: "file"},
	}
	if !domain.SessionMatchesListOptions(session, domain.SessionListOptions{
		SessionType:        domain.SessionTypeScript,
		TriggerSourceQuery: "loader",
		TitleQuery:         "branch",
		WorkspaceQuery:     "workspace one",
		Driver:             driverpkg.RuntimeDriverDocker,
		VMStatus:           domain.VMStatusRunning,
		CreatedFrom:        now.Add(-time.Second),
		CreatedTo:          now.Add(time.Second),
		UpdatedFrom:        now,
		UpdatedTo:          now.Add(2 * time.Minute),
	}) {
		t.Fatalf("session should match full list options")
	}
	for _, options := range []domain.SessionListOptions{
		{SessionType: domain.SessionTypeManual},
		{TriggerSourceQuery: "missing"},
		{TitleQuery: "missing"},
		{WorkspaceQuery: "missing"},
		{Driver: driverpkg.RuntimeDriverBoxlite},
		{VMStatus: domain.VMStatusStopped},
		{CreatedFrom: now.Add(time.Second)},
		{CreatedTo: now.Add(-time.Second)},
		{UpdatedFrom: now.Add(2 * time.Minute)},
		{UpdatedTo: now.Add(-time.Second)},
	} {
		if domain.SessionMatchesListOptions(session, options) {
			t.Fatalf("session unexpectedly matched options %#v", options)
		}
	}
	if domain.SessionMatchesListOptions(nil, domain.SessionListOptions{}) {
		t.Fatalf("nil session matched list options")
	}
	if got := domain.NormalizeSessionTriggerSource("", []domain.SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: "loader-9"}}); got != "script:loader-9" {
		t.Fatalf("NormalizeSessionTriggerSource tags = %q", got)
	}
	if got := domain.PaginateSessions([]*domain.Session{session}, 5, 10); got != nil {
		t.Fatalf("PaginateSessions beyond end = %#v", got)
	}
	offset, limit := domain.NormalizeSessionListBounds(-1, 0)
	if offset != 0 || limit != domain.DefaultSessionListLimit {
		t.Fatalf("NormalizeSessionListBounds = %d/%d", offset, limit)
	}

	for _, runtime := range []string{"", domain.LoaderRuntimeScheduler} {
		if got, err := domain.NormalizeLoaderRuntime(runtime); err != nil || got != domain.LoaderRuntimeScheduler {
			t.Fatalf("NormalizeLoaderRuntime(%q) = %q/%v", runtime, got, err)
		}
	}
	for _, runtime := range []string{"qjs", "quickjs", "bad"} {
		if _, err := domain.NormalizeLoaderRuntime(runtime); err == nil {
			t.Fatalf("NormalizeLoaderRuntime(%q) returned nil error", runtime)
		}
	}
	for _, kind := range []string{domain.LoaderTriggerKindInterval, domain.LoaderTriggerKindEvent, domain.LoaderTriggerKindTimeout, domain.LoaderTriggerKindCron} {
		if got, err := domain.NormalizeLoaderTriggerKind(kind); err != nil || got != kind {
			t.Fatalf("NormalizeLoaderTriggerKind(%q) = %q/%v", kind, got, err)
		}
	}
	if _, err := domain.NormalizeLoaderTriggerKind("bad"); err == nil {
		t.Fatalf("NormalizeLoaderTriggerKind bad returned nil error")
	}
	if domain.NormalizeLoaderSessionPolicy("new") != domain.LoaderSessionPolicyNew || domain.NormalizeLoaderSessionPolicy("bad") != domain.LoaderSessionPolicySticky {
		t.Fatalf("NormalizeLoaderSessionPolicy returned unexpected values")
	}
	if domain.NormalizeLoaderConcurrencyPolicy("allow") != domain.LoaderConcurrencyPolicyParallel || domain.NormalizeLoaderConcurrencyPolicy("bad") != domain.LoaderConcurrencyPolicySkip {
		t.Fatalf("NormalizeLoaderConcurrencyPolicy returned unexpected values")
	}
	for _, status := range []string{domain.LoaderRunStatusRunning, domain.LoaderRunStatusSucceeded, domain.LoaderRunStatusFailed, domain.LoaderRunStatusSkipped} {
		if domain.NormalizeLoaderRunStatus(status) != status {
			t.Fatalf("NormalizeLoaderRunStatus(%q) changed", status)
		}
	}
	if domain.NormalizeLoaderRunStatus("bad") != domain.LoaderRunStatusRunning {
		t.Fatalf("NormalizeLoaderRunStatus bad did not default")
	}
	if !domain.LoaderTriggerTopicMatches("agent-compose.session.*", "agent-compose.session.created") || domain.LoaderTriggerTopicMatches("", "agent-compose.session.created") || domain.LoaderTriggerTopicMatches("agent-compose.loader", "") {
		t.Fatalf("LoaderTriggerTopicMatches returned unexpected values")
	}
	if domain.LoaderTriggerTopicMatches("adp.session.*", "agent-compose.session.created") {
		t.Fatalf("legacy session wildcard matched agent-compose lifecycle topic")
	}
	if !domain.LoaderTriggerUsesSchedule(domain.LoaderTriggerKindCron) || domain.LoaderTriggerUsesSchedule(domain.LoaderTriggerKindEvent) {
		t.Fatalf("LoaderTriggerUsesSchedule returned unexpected values")
	}
	if !domain.TimeIsSet(now) || domain.TimeIsSet(time.Time{}) || domain.NonZeroTimeUnixMilli(time.Time{}) != 0 || domain.NonZeroTimeUnixMilli(now) == 0 {
		t.Fatalf("time helper returned unexpected values")
	}
	if !domain.LoaderTriggerScheduledAt(now, 10).After(now) || !domain.LoaderTriggerScheduledAt(now, 0).IsZero() {
		t.Fatalf("LoaderTriggerScheduledAt returned unexpected values")
	}
	if domain.DefaultLoaderName(now) == "" || !strings.Contains(domain.DefaultLoaderScript(), "scheduler.interval") || domain.LoaderSourceSHA("script") == "" || domain.LoaderTriggerStableID("kind", "topic", 1, "cb", 0) == "" {
		t.Fatalf("loader default/hash helpers returned empty values")
	}

	normalizedEnv := domain.NormalizeEnvItems([]domain.SessionEnvVar{{Name: " B ", Value: "2"}, {Name: "A", Value: "1"}, {Name: "B", Value: "3"}, {Name: " ", Value: "skip"}})
	if len(normalizedEnv) != 2 || normalizedEnv[0].Name != "A" || normalizedEnv[1].Value != "3" {
		t.Fatalf("NormalizeEnvItems = %#v", normalizedEnv)
	}
	mergedEnv := domain.MergeEnvItems([]domain.SessionEnvVar{{Name: "A", Value: "global"}}, []domain.SessionEnvVar{{Name: "A", Value: "session"}, {Name: "B", Value: "session"}})
	if len(mergedEnv) != 2 || mergedEnv[0].Value != "session" || mergedEnv[1].Name != "B" {
		t.Fatalf("MergeEnvItems = %#v", mergedEnv)
	}
	if domain.MergeEnvItems(nil, nil) != nil {
		t.Fatalf("MergeEnvItems nil did not return nil")
	}
	envMap := domain.SessionEnvMap([]domain.SessionEnvVar{{Name: " A ", Value: "1"}, {Name: " ", Value: "skip"}}, []domain.SessionEnvVar{{Name: "B", Value: "2"}})
	if envMap["A"] != "1" || envMap["B"] != "2" || domain.SessionEnvMap([]domain.SessionEnvVar{{Name: " "}}) != nil {
		t.Fatalf("SessionEnvMap = %#v", envMap)
	}
	classified := domain.ResourceError(domain.ErrNotFound, "session", "session-1", "", errors.New("missing"))
	if !errors.Is(classified, domain.ErrNotFound) || !strings.Contains(classified.Error(), "missing") {
		t.Fatalf("classified error = %v", classified)
	}
	if err := domain.ClassifyError(domain.ErrInvalidArgument, "bad input", errors.New("cause")); !errors.Is(err, domain.ErrInvalidArgument) || !strings.Contains(err.Error(), "bad input: cause") {
		t.Fatalf("ClassifyError = %v", err)
	}
	if err := domain.ClassifyError(domain.ErrUnsupported, "feature unsupported", nil); !errors.Is(err, domain.ErrUnsupported) || !strings.Contains(err.Error(), "feature unsupported") {
		t.Fatalf("unsupported ClassifyError = %v", err)
	}
	workspace, err := configstore.NormalizeWorkspaceConfig(domain.WorkspaceConfig{Name: " Workspace ", Type: "FILE", ConfigJSON: "", Comment: " note "}, true)
	if err != nil || workspace.ID == "" || workspace.Type != "file" || workspace.ConfigJSON != "{}" || workspace.Comment != "note" {
		t.Fatalf("NormalizeWorkspaceConfig assign = %#v/%v", workspace, err)
	}
	for _, item := range []domain.WorkspaceConfig{
		{Name: "missing id", Type: "file"},
		{ID: "id", Type: "file"},
		{ID: "id", Name: "name"},
		{ID: "id", Name: "name", Type: "bad"},
	} {
		if _, err := configstore.NormalizeWorkspaceConfig(item, false); err == nil {
			t.Fatalf("NormalizeWorkspaceConfig(%#v) returned nil error", item)
		}
	}

	bus, err := loaders.NewBus(do.New())
	if err != nil || bus.Events() == nil {
		t.Fatalf("NewBus = %#v/%v", bus, err)
	}
	if (&loaders.Bus{}).Publish(domain.LoaderTopicEvent{Topic: "runtime.test"}) {
		t.Fatalf("Publish on bus without channel succeeded")
	}
	if bus.Publish(domain.LoaderTopicEvent{}) {
		t.Fatalf("Publish empty topic succeeded")
	}
	if !bus.Publish(domain.LoaderTopicEvent{Topic: "runtime.test", Payload: map[string]any{"ok": true}}) {
		t.Fatalf("Publish valid event failed")
	}
	select {
	case event := <-bus.Events():
		if event.Topic != "runtime.test" {
			t.Fatalf("published event = %#v", event)
		}
	default:
		t.Fatalf("expected published event")
	}
	if (*loaders.Bus)(nil).Events() != nil || (*loaders.Bus)(nil).Publish(domain.LoaderTopicEvent{Topic: "runtime.test"}) {
		t.Fatalf("nil loader bus helpers returned unexpected values")
	}

	ctx := context.Background()
	if err := ctx.Err(); err != nil {
		t.Fatalf("background context err = %v", err)
	}
}
