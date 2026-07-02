package agentcompose

import (
	driverpkg "agent-compose/internal/driver"
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
			VMStatus:      VMStatusRunning,
			WorkspacePath: "/workspaces/branch",
			CreatedAt:     now,
			UpdatedAt:     now.Add(time.Minute),
		},
		WorkspaceID: "workspace-1",
		Workspace:   &SessionWorkspace{ID: "workspace-1", Name: "Workspace One", Type: "file"},
	}
	if !sessionMatchesListOptions(session, SessionListOptions{
		SessionType:        SessionTypeScript,
		TriggerSourceQuery: "loader",
		TitleQuery:         "branch",
		WorkspaceQuery:     "workspace one",
		Driver:             driverpkg.RuntimeDriverDocker,
		VMStatus:           VMStatusRunning,
		CreatedFrom:        now.Add(-time.Second),
		CreatedTo:          now.Add(time.Second),
		UpdatedFrom:        now,
		UpdatedTo:          now.Add(2 * time.Minute),
	}) {
		t.Fatalf("session should match full list options")
	}
	for _, options := range []SessionListOptions{
		{SessionType: SessionTypeManual},
		{TriggerSourceQuery: "missing"},
		{TitleQuery: "missing"},
		{WorkspaceQuery: "missing"},
		{Driver: driverpkg.RuntimeDriverBoxlite},
		{VMStatus: VMStatusStopped},
		{CreatedFrom: now.Add(time.Second)},
		{CreatedTo: now.Add(-time.Second)},
		{UpdatedFrom: now.Add(2 * time.Minute)},
		{UpdatedTo: now.Add(-time.Second)},
	} {
		if sessionMatchesListOptions(session, options) {
			t.Fatalf("session unexpectedly matched options %#v", options)
		}
	}
	if sessionMatchesListOptions(nil, SessionListOptions{}) {
		t.Fatalf("nil session matched list options")
	}
	if got := normalizeSessionTriggerSource("", []SessionTag{{Name: "origin", Value: "loader"}, {Name: "loader_id", Value: "loader-9"}}); got != "script:loader-9" {
		t.Fatalf("normalizeSessionTriggerSource tags = %q", got)
	}
	if got := paginateSessions([]*Session{session}, 5, 10); got != nil {
		t.Fatalf("paginateSessions beyond end = %#v", got)
	}
	offset, limit := normalizeSessionListBounds(-1, 0)
	if offset != 0 || limit != defaultSessionListLimit {
		t.Fatalf("normalizeSessionListBounds = %d/%d", offset, limit)
	}

	parsed, err := sessionListOptionsFromProto(&agentcomposev1.ListSessionsRequest{
		SessionType:        SessionTypeScript,
		TriggerSourceQuery: "script",
		TitleQuery:         "title",
		WorkspaceQuery:     "workspace",
		Driver:             driverpkg.RuntimeDriverDocker,
		VmStatus:           VMStatusRunning,
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

	for _, runtime := range []string{"", LoaderRuntimeScheduler} {
		if got, err := normalizeLoaderRuntime(runtime); err != nil || got != LoaderRuntimeScheduler {
			t.Fatalf("normalizeLoaderRuntime(%q) = %q/%v", runtime, got, err)
		}
	}
	for _, runtime := range []string{"qjs", "quickjs", "bad"} {
		if _, err := normalizeLoaderRuntime(runtime); err == nil {
			t.Fatalf("normalizeLoaderRuntime(%q) returned nil error", runtime)
		}
	}
	for _, kind := range []string{LoaderTriggerKindInterval, LoaderTriggerKindEvent, LoaderTriggerKindTimeout, LoaderTriggerKindCron} {
		if got, err := normalizeLoaderTriggerKind(kind); err != nil || got != kind {
			t.Fatalf("normalizeLoaderTriggerKind(%q) = %q/%v", kind, got, err)
		}
	}
	if _, err := normalizeLoaderTriggerKind("bad"); err == nil {
		t.Fatalf("normalizeLoaderTriggerKind bad returned nil error")
	}
	if normalizeLoaderSessionPolicy("new") != LoaderSessionPolicyNew || normalizeLoaderSessionPolicy("bad") != LoaderSessionPolicySticky {
		t.Fatalf("normalizeLoaderSessionPolicy returned unexpected values")
	}
	if normalizeLoaderConcurrencyPolicy("allow") != LoaderConcurrencyPolicyParallel || normalizeLoaderConcurrencyPolicy("bad") != LoaderConcurrencyPolicySkip {
		t.Fatalf("normalizeLoaderConcurrencyPolicy returned unexpected values")
	}
	for _, status := range []string{LoaderRunStatusRunning, LoaderRunStatusSucceeded, LoaderRunStatusFailed, LoaderRunStatusSkipped} {
		if normalizeLoaderRunStatus(status) != status {
			t.Fatalf("normalizeLoaderRunStatus(%q) changed", status)
		}
	}
	if normalizeLoaderRunStatus("bad") != LoaderRunStatusRunning {
		t.Fatalf("normalizeLoaderRunStatus bad did not default")
	}
	if !loaderTriggerTopicMatches("agent-compose.session.*", "agent-compose.session.created") || loaderTriggerTopicMatches("", "agent-compose.session.created") || loaderTriggerTopicMatches("agent-compose.loader", "") {
		t.Fatalf("loaderTriggerTopicMatches returned unexpected values")
	}
	legacySessionWildcard := "a" + "dp.session.*"
	if loaderTriggerTopicMatches(legacySessionWildcard, "agent-compose.session.created") {
		t.Fatalf("legacy session wildcard matched agent-compose lifecycle topic")
	}
	if !loaderTriggerUsesSchedule(LoaderTriggerKindCron) || loaderTriggerUsesSchedule(LoaderTriggerKindEvent) {
		t.Fatalf("loaderTriggerUsesSchedule returned unexpected values")
	}
	if !timeIsSet(now) || timeIsSet(time.Time{}) || nonZeroTimeUnixMilli(time.Time{}) != 0 || nonZeroTimeUnixMilli(now) == 0 {
		t.Fatalf("time helper returned unexpected values")
	}
	if loaderTriggerScheduledAt(now, 0).IsZero() == false || !loaderTriggerScheduledAt(now, 10).After(now) {
		t.Fatalf("loaderTriggerScheduledAt returned unexpected values")
	}
	if defaultLoaderName(now) == "" || !strings.Contains(defaultLoaderScript(), "scheduler.interval") || loaderSourceSHA("script") == "" || loaderTriggerStableID("kind", "topic", 1, "cb", 0) == "" {
		t.Fatalf("loader default/hash helpers returned empty values")
	}

	if got := sessionEnvMap([]SessionEnvVar{{Name: " A ", Value: "1"}, {Name: " ", Value: "skip"}}); got["A"] != "1" || len(got) != 1 {
		t.Fatalf("sessionEnvMap = %#v", got)
	}
	if sessionEnvMap(nil) != nil {
		t.Fatalf("sessionEnvMap nil did not return nil")
	}
	normalizedEnv := normalizeEnvItems([]SessionEnvVar{{Name: " B ", Value: "2"}, {Name: "A", Value: "1"}, {Name: "B", Value: "3"}, {Name: " ", Value: "skip"}})
	if len(normalizedEnv) != 2 || normalizedEnv[0].Name != "A" || normalizedEnv[1].Value != "3" {
		t.Fatalf("normalizeEnvItems = %#v", normalizedEnv)
	}
	mergedEnv := mergeEnvItems([]SessionEnvVar{{Name: "A", Value: "global"}}, []SessionEnvVar{{Name: "A", Value: "session"}, {Name: "B", Value: "session"}})
	if len(mergedEnv) != 2 || mergedEnv[0].Value != "session" || mergedEnv[1].Name != "B" {
		t.Fatalf("mergeEnvItems = %#v", mergedEnv)
	}
	if mergeEnvItems(nil, nil) != nil {
		t.Fatalf("mergeEnvItems nil did not return nil")
	}
	workspace, err := normalizeWorkspaceConfig(WorkspaceConfig{Name: " Workspace ", Type: "FILE", ConfigJSON: "", Comment: " note "}, true)
	if err != nil || workspace.ID == "" || workspace.Type != "file" || workspace.ConfigJSON != "{}" || workspace.Comment != "note" {
		t.Fatalf("normalizeWorkspaceConfig assign = %#v/%v", workspace, err)
	}
	for _, item := range []WorkspaceConfig{
		{Name: "missing id", Type: "file"},
		{ID: "id", Type: "file"},
		{ID: "id", Name: "name"},
		{ID: "id", Name: "name", Type: "bad"},
	} {
		if _, err := normalizeWorkspaceConfig(item, false); err == nil {
			t.Fatalf("normalizeWorkspaceConfig(%#v) returned nil error", item)
		}
	}

	bus, err := NewLoaderBus(do.New())
	if err != nil || bus.Events() == nil {
		t.Fatalf("NewLoaderBus = %#v/%v", bus, err)
	}
	if (&LoaderBus{}).Publish(LoaderTopicEvent{Topic: "runtime.test"}) {
		t.Fatalf("Publish on bus without channel succeeded")
	}
	if bus.Publish(LoaderTopicEvent{}) {
		t.Fatalf("Publish empty topic succeeded")
	}
	if !bus.Publish(LoaderTopicEvent{Topic: "runtime.test", Payload: map[string]any{"ok": true}}) {
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
	if (*LoaderBus)(nil).Events() != nil || (*LoaderBus)(nil).Publish(LoaderTopicEvent{Topic: "runtime.test"}) {
		t.Fatalf("nil loader bus helpers returned unexpected values")
	}

	serviceBus := &LoaderBus{ch: make(chan LoaderTopicEvent, 1)}
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
	if sessionTopicPayload(nil, "test") != nil {
		t.Fatalf("sessionTopicPayload nil did not return nil")
	}
	sessionPayload := sessionTopicPayload(session, "test")
	if sessionPayload["sessionId"] != "session-branch" || sessionPayload["source"] != "test" {
		t.Fatalf("session topic payload = %#v", sessionPayload)
	}
	cellPayload := cellTopicPayload("session-branch", NotebookCell{ID: "cell-1", Type: CellTypeShell, Agent: "codex", Success: true}, "test")
	if cellPayload["cellId"] != "cell-1" || cellPayload["source"] != "test" {
		t.Fatalf("cell topic payload = %#v", cellPayload)
	}
	commandPayload := loaderCommandEventPayload(LoaderCommandRequest{Mode: "shell", Command: "ignored", Args: []string{"-c"}, Cwd: "/tmp"}, LoaderCommandResult{ExitCode: 2, Success: false, SessionID: "session-branch", CellID: "cell-1"})
	if commandPayload["command"] != "" || commandPayload["exitCode"] != 2 {
		t.Fatalf("command event payload = %#v", commandPayload)
	}

	if _, err := driverpkg.EnsureDockerImage(ctx, " "); err != nil {
		t.Fatalf("driverpkg.EnsureDockerImage blank returned error: %v", err)
	}
}
