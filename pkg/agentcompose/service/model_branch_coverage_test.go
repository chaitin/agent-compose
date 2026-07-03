package agentcompose

import (
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

func TestModelSessionConfigAndBusBranchCoverage(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 2, 8, 0, 0, 0, time.UTC)
	session := &Session{
		Summary: SessionSummary{
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
		Workspace:   &SessionWorkspace{ID: "workspace-1", Name: "Workspace One", Type: "file"},
	}
	if !domain.SessionMatchesListOptions(session, SessionListOptions{
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
	for _, options := range []SessionListOptions{
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
	if domain.SessionMatchesListOptions(nil, SessionListOptions{}) {
		t.Fatalf("nil session matched list options")
	}
	if got := domain.NormalizeSessionTriggerSource("", []SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: "loader-9"}}); got != "script:loader-9" {
		t.Fatalf("normalizeSessionTriggerSource tags = %q", got)
	}
	if got := domain.PaginateSessions([]*Session{session}, 5, 10); got != nil {
		t.Fatalf("paginateSessions beyond end = %#v", got)
	}
	offset, limit := domain.NormalizeSessionListBounds(-1, 0)
	if offset != 0 || limit != domain.DefaultSessionListLimit {
		t.Fatalf("normalizeSessionListBounds = %d/%d", offset, limit)
	}

	parsed, err := sessionListOptionsFromProto(&agentcomposev1.ListSessionsRequest{
		SessionType:        domain.SessionTypeScript,
		TriggerSourceQuery: "script",
		TitleQuery:         "title",
		WorkspaceQuery:     "workspace",
		Driver:             driverpkg.RuntimeDriverDocker,
		VmStatus:           domain.VMStatusRunning,
		CreatedFrom:        now.Format(time.RFC3339),
		CreatedTo:          now.Add(time.Hour).Format(time.RFC3339),
		UpdatedFrom:        now.Format(time.RFC3339),
		UpdatedTo:          now.Add(time.Hour).Format(time.RFC3339),
		Offset:             3,
		Limit:              7,
	})
	if err != nil {
		t.Fatalf("sessionListOptionsFromProto returned error: %v", err)
	}
	if parsed.Offset != 3 || parsed.Limit != 7 || parsed.CreatedFrom.IsZero() || parsed.UpdatedTo.IsZero() {
		t.Fatalf("parsed session options = %#v", parsed)
	}
	if _, err := sessionListOptionsFromProto(&agentcomposev1.ListSessionsRequest{CreatedFrom: "bad"}); err == nil {
		t.Fatalf("invalid created_from returned nil error")
	}
	if value, err := parseOptionalRFC3339(" ", "field"); err != nil || !value.IsZero() {
		t.Fatalf("parseOptionalRFC3339 blank = %s/%v", value, err)
	}

	for _, runtime := range []string{"", domain.LoaderRuntimeScheduler} {
		if got, err := domain.NormalizeLoaderRuntime(runtime); err != nil || got != domain.LoaderRuntimeScheduler {
			t.Fatalf("domain.NormalizeLoaderRuntime(%q) = %q/%v", runtime, got, err)
		}
	}
	for _, runtime := range []string{"qjs", "quickjs", "bad"} {
		if _, err := domain.NormalizeLoaderRuntime(runtime); err == nil {
			t.Fatalf("domain.NormalizeLoaderRuntime(%q) returned nil error", runtime)
		}
	}
	for _, kind := range []string{domain.LoaderTriggerKindInterval, domain.LoaderTriggerKindEvent, domain.LoaderTriggerKindTimeout, domain.LoaderTriggerKindCron} {
		if got, err := domain.NormalizeLoaderTriggerKind(kind); err != nil || got != kind {
			t.Fatalf("domain.NormalizeLoaderTriggerKind(%q) = %q/%v", kind, got, err)
		}
	}
	if _, err := domain.NormalizeLoaderTriggerKind("bad"); err == nil {
		t.Fatalf("normalizeLoaderTriggerKind bad returned nil error")
	}
	if domain.NormalizeLoaderSessionPolicy("new") != domain.LoaderSessionPolicyNew || domain.NormalizeLoaderSessionPolicy("bad") != domain.LoaderSessionPolicySticky {
		t.Fatalf("normalizeLoaderSessionPolicy returned unexpected values")
	}
	if domain.NormalizeLoaderConcurrencyPolicy("allow") != domain.LoaderConcurrencyPolicyParallel || domain.NormalizeLoaderConcurrencyPolicy("bad") != domain.LoaderConcurrencyPolicySkip {
		t.Fatalf("normalizeLoaderConcurrencyPolicy returned unexpected values")
	}
	for _, status := range []string{domain.LoaderRunStatusRunning, domain.LoaderRunStatusSucceeded, domain.LoaderRunStatusFailed, domain.LoaderRunStatusSkipped} {
		if domain.NormalizeLoaderRunStatus(status) != status {
			t.Fatalf("domain.NormalizeLoaderRunStatus(%q) changed", status)
		}
	}
	if domain.NormalizeLoaderRunStatus("bad") != domain.LoaderRunStatusRunning {
		t.Fatalf("normalizeLoaderRunStatus bad did not default")
	}
	if !domain.LoaderTriggerTopicMatches("agent-compose.session.*", "agent-compose.session.created") || domain.LoaderTriggerTopicMatches("", "agent-compose.session.created") || domain.LoaderTriggerTopicMatches("agent-compose.loader", "") {
		t.Fatalf("loaderTriggerTopicMatches returned unexpected values")
	}
	legacySessionWildcard := "a" + "dp.session.*"
	if domain.LoaderTriggerTopicMatches(legacySessionWildcard, "agent-compose.session.created") {
		t.Fatalf("legacy session wildcard matched agent-compose lifecycle topic")
	}
	if !domain.LoaderTriggerUsesSchedule(domain.LoaderTriggerKindCron) || domain.LoaderTriggerUsesSchedule(domain.LoaderTriggerKindEvent) {
		t.Fatalf("loaderTriggerUsesSchedule returned unexpected values")
	}
	if !domain.TimeIsSet(now) || domain.TimeIsSet(time.Time{}) || domain.NonZeroTimeUnixMilli(time.Time{}) != 0 || domain.NonZeroTimeUnixMilli(now) == 0 {
		t.Fatalf("time helper returned unexpected values")
	}
	if domain.LoaderTriggerScheduledAt(now, 0).IsZero() == false || !domain.LoaderTriggerScheduledAt(now, 10).After(now) {
		t.Fatalf("loaderTriggerScheduledAt returned unexpected values")
	}
	if domain.DefaultLoaderName(now) == "" || !strings.Contains(domain.DefaultLoaderScript(), "scheduler.interval") || domain.LoaderSourceSHA("script") == "" || domain.LoaderTriggerStableID("kind", "topic", 1, "cb", 0) == "" {
		t.Fatalf("loader default/hash helpers returned empty values")
	}

	if got := sessionEnvMap([]SessionEnvVar{{Name: " A ", Value: "1"}, {Name: " ", Value: "skip"}}); got["A"] != "1" || len(got) != 1 {
		t.Fatalf("sessionEnvMap = %#v", got)
	}
	if sessionEnvMap(nil) != nil {
		t.Fatalf("sessionEnvMap nil did not return nil")
	}
	normalizedEnv := domain.NormalizeEnvItems([]SessionEnvVar{{Name: " B ", Value: "2"}, {Name: "A", Value: "1"}, {Name: "B", Value: "3"}, {Name: " ", Value: "skip"}})
	if len(normalizedEnv) != 2 || normalizedEnv[0].Name != "A" || normalizedEnv[1].Value != "3" {
		t.Fatalf("normalizeEnvItems = %#v", normalizedEnv)
	}
	mergedEnv := domain.MergeEnvItems([]SessionEnvVar{{Name: "A", Value: "global"}}, []SessionEnvVar{{Name: "A", Value: "session"}, {Name: "B", Value: "session"}})
	if len(mergedEnv) != 2 || mergedEnv[0].Value != "session" || mergedEnv[1].Name != "B" {
		t.Fatalf("mergeEnvItems = %#v", mergedEnv)
	}
	if domain.MergeEnvItems(nil, nil) != nil {
		t.Fatalf("mergeEnvItems nil did not return nil")
	}
	workspace, err := configstore.NormalizeWorkspaceConfig(WorkspaceConfig{Name: " Workspace ", Type: "FILE", ConfigJSON: "", Comment: " note "}, true)
	if err != nil || workspace.ID == "" || workspace.Type != "file" || workspace.ConfigJSON != "{}" || workspace.Comment != "note" {
		t.Fatalf("normalizeWorkspaceConfig assign = %#v/%v", workspace, err)
	}
	for _, item := range []WorkspaceConfig{
		{Name: "missing id", Type: "file"},
		{ID: "id", Type: "file"},
		{ID: "id", Name: "name"},
		{ID: "id", Name: "name", Type: "bad"},
	} {
		if _, err := configstore.NormalizeWorkspaceConfig(item, false); err == nil {
			t.Fatalf("normalizeWorkspaceConfig(%#v) returned nil error", item)
		}
	}

	bus, err := loaders.NewBus(do.New())
	if err != nil || bus.Events() == nil {
		t.Fatalf("NewLoaderBus = %#v/%v", bus, err)
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

	serviceBus := newTestLoaderBus(1)
	service := &Service{bus: serviceBus}
	service.publishLoaderTopic("agent-compose.session.test", map[string]any{"sessionId": "session-branch"})
	select {
	case event := <-serviceBus.Events():
		if event.Topic != "agent-compose.session.test" {
			t.Fatalf("service topic event = %#v", event)
		}
	default:
		t.Fatalf("expected service topic event")
	}
	(*Service)(nil).publishLoaderTopic("agent-compose.missing", nil)
	if loaders.SessionTopicPayload(nil, "test") != nil {
		t.Fatalf("sessionTopicPayload nil did not return nil")
	}
	sessionPayload := loaders.SessionTopicPayload(session, "test")
	if sessionPayload["sessionId"] != "session-branch" || sessionPayload["source"] != "test" {
		t.Fatalf("session topic payload = %#v", sessionPayload)
	}
	cellPayload := loaders.CellTopicPayload("session-branch", NotebookCell{ID: "cell-1", Type: execution.CellTypeShell, Agent: "codex", Success: true}, "test")
	if cellPayload["cellId"] != "cell-1" || cellPayload["source"] != "test" {
		t.Fatalf("cell topic payload = %#v", cellPayload)
	}
	commandPayload := loaders.CommandEventPayload(domain.LoaderCommandRequest{Mode: "shell", Command: "ignored", Args: []string{"-c"}, Cwd: "/tmp"}, domain.LoaderCommandResult{ExitCode: 2, Success: false, SessionID: "session-branch", CellID: "cell-1"})
	if commandPayload["command"] != "" || commandPayload["exitCode"] != 2 {
		t.Fatalf("command event payload = %#v", commandPayload)
	}

	if _, err := driverpkg.EnsureDockerImage(ctx, " "); err != nil {
		t.Fatalf("driverpkg.EnsureDockerImage blank returned error: %v", err)
	}
}
