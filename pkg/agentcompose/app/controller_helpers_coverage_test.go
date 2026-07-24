package app

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/events/webhooks"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestAppProjectControllerHelperCoverage(t *testing.T) {
	if normalized, issues, err := normalizeProjectRequest(nil, nil, ""); err != nil || normalized.Spec != nil || len(issues) != 1 {
		t.Fatalf("normalizeProjectRequest nil normalized=%#v issues=%#v err=%v", normalized, issues, err)
	}
	dupSpec := &agentcomposev2.ProjectSpec{Variables: []*agentcomposev2.EnvVarSpec{{Name: "A"}, {Name: " A "}}}
	if _, issues, err := normalizeProjectRequest(dupSpec, nil, ""); err != nil || len(issues) != 1 || issues[0].Path == "" {
		t.Fatalf("normalizeProjectRequest duplicate issues=%#v err=%v", issues, err)
	}
	validSpec := &agentcomposev2.ProjectSpec{
		Name: "app-project",
		Workspaces: []*agentcomposev2.NamedWorkspaceSpec{{
			Name:      "default",
			Workspace: &agentcomposev2.WorkspaceSpec{Provider: "file", Path: "."},
		}},
		Agents: []*agentcomposev2.AgentSpec{{
			Name:     "worker",
			Provider: "codex",
			Model:    "gpt",
		}},
	}
	normalized, issues, err := normalizeProjectRequest(validSpec, &agentcomposev2.ProjectSource{ProjectDir: "/repo"}, "mismatch")
	if err != nil || normalized.Spec == nil || len(issues) != 1 || issues[0].Path != "expected_spec_hash" {
		t.Fatalf("normalizeProjectRequest mismatch normalized=%#v issues=%#v err=%v", normalized, issues, err)
	}
	normalized, issues, err = normalizeProjectRequest(validSpec, &agentcomposev2.ProjectSource{ComposePath: "/repo/custom.yml"}, "")
	if err != nil || len(issues) != 0 || normalized.SourcePath != "/repo/custom.yml" || normalizedSpecToProto(normalized.Spec).GetName() != "app-project" {
		t.Fatalf("normalizeProjectRequest valid normalized=%#v issues=%#v err=%v", normalized, issues, err)
	}
	if normalizedSpecToProto(nil) != nil {
		t.Fatalf("normalizedSpecToProto nil returned non-nil")
	}
	if ref := projectRefFromProto(&agentcomposev2.ProjectRef{ProjectId: "project-1", Name: "Project", SourcePath: "/repo"}); ref.ProjectID != "project-1" || ref.Name != "Project" || ref.SourcePath != "/repo" {
		t.Fatalf("projectRefFromProto = %#v", ref)
	}
	if ref := projectRefFromProto(nil); ref != (projects.ProjectRef{}) {
		t.Fatalf("projectRefFromProto nil = %#v", ref)
	}
	protoIssues := validationIssuesToProto([]projects.ValidationIssue{{Path: "path", Message: "bad"}})
	if got := validationIssuesFromProto(append(protoIssues, nil)); len(got) != 2 || got[0].Path != "path" {
		t.Fatalf("validationIssues round trip = %#v", got)
	}
	warnings := validationIssuesToProto([]projects.ValidationIssue{{Severity: projects.ValidationSeverityWarning, Path: "agents.capset_ids", Message: "warning"}})
	if len(warnings) != 1 || warnings[0].GetSeverity() != agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_WARNING {
		t.Fatalf("warning severity = %#v", warnings)
	}
	if got := validationIssuesFromProto(warnings); len(got) != 1 || got[0].Severity != projects.ValidationSeverityWarning {
		t.Fatalf("warning round trip = %#v", got)
	}
	changes := projectChangesToProto([]projects.Change{
		{Action: projects.ChangeActionCreated, ResourceType: "project", ResourceID: "created"},
		{Action: projects.ChangeActionUpdated, ResourceType: "project", ResourceID: "updated"},
		{Action: projects.ChangeActionRemoved, ResourceType: "project", ResourceID: "removed"},
		{Action: projects.ChangeActionUnchanged, ResourceType: "project", ResourceID: "same"},
		{Action: "other", ResourceType: "project", ResourceID: "other"},
	})
	if len(changes) != 5 || changes[0].GetAction() != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED || changes[4].GetAction() != agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNSPECIFIED {
		t.Fatalf("projectChangesToProto = %#v", changes)
	}
	for _, tc := range []struct {
		err  error
		code connect.Code
	}{
		{nil, 0},
		{projects.ErrInvalidRequest, connect.CodeInvalidArgument},
		{domain.ErrAmbiguous, connect.CodeInvalidArgument},
		{projects.ErrUnavailable, connect.CodeUnavailable},
		{projects.ErrUnimplemented, connect.CodeUnimplemented},
		{domain.ErrUnsupported, connect.CodeUnimplemented},
		{sql.ErrNoRows, connect.CodeNotFound},
		{domain.ErrNotFound, connect.CodeNotFound},
		{errors.New("boom"), connect.CodeInternal},
	} {
		mapped := projectConnectError(tc.err)
		if tc.err == nil {
			if mapped != nil {
				t.Fatalf("projectConnectError nil = %v", mapped)
			}
			continue
		}
		if connect.CodeOf(mapped) != tc.code {
			t.Fatalf("projectConnectError(%v) code=%s want %s", tc.err, connect.CodeOf(mapped), tc.code)
		}
	}
}

func TestCapabilityRuntimeConfigCoverage(t *testing.T) {
	if got := (capabilityRuntimeConfig{}).CapProxyListen(); got != "" {
		t.Fatalf("nil config CapProxyListen = %q", got)
	}
	if got := (capabilityRuntimeConfig{config: &appconfig.Config{CapGRPCListen: "127.0.0.1:9100"}}).CapProxyListen(); got != "127.0.0.1:9100" {
		t.Fatalf("CapProxyListen = %q", got)
	}
}

func TestAppRunControllerHelperCoverage(t *testing.T) {
	msg := &agentcomposev2.RunAgentRequest{
		ProjectId:        "project-1",
		AgentName:        "worker",
		Prompt:           "prompt",
		Command:          "command",
		Source:           agentcomposev2.RunSource_RUN_SOURCE_API,
		SchedulerId:      "scheduler-1",
		TriggerId:        "trigger-1",
		ClientRequestId:  "request-1",
		Env:              []*agentcomposev2.EnvVarSpec{{Name: "A", Value: "B"}},
		SandboxId:        "session-1",
		Driver:           "docker",
		OutputSchemaJson: `{"type":"object"}`,
		CleanupPolicy:    agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		Jupyter:          &agentcomposev2.RunJupyterSpec{Enabled: true},
	}
	req := runAgentRequestFromProto(msg)
	if req.ProjectID != "project-1" || req.Source != domain.ProjectRunSourceAPI || req.Jupyter == nil || len(req.Env) != 1 {
		t.Fatalf("runAgentRequestFromProto = %#v", req)
	}
	if _, err := (runControllerDelegate{}).StartRun(context.Background(), connect.NewRequest(&agentcomposev2.StartRunRequest{Run: msg})); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("StartRun nil supervisor err=%v", err)
	}
	for _, tc := range []struct {
		err  error
		code connect.Code
	}{
		{nil, 0},
		{runs.ErrInvalidRequest, connect.CodeInvalidArgument},
		{domain.ErrUnsupported, connect.CodeUnimplemented},
		{domain.ErrNotFound, connect.CodeNotFound},
		{domain.ErrConflict, connect.CodeFailedPrecondition},
		{domain.ErrFailedPrecondition, connect.CodeFailedPrecondition},
		{errors.New("boom"), connect.CodeInternal},
	} {
		mapped := runConnectError(tc.err)
		if tc.err == nil {
			if mapped != nil {
				t.Fatalf("runConnectError nil = %v", mapped)
			}
			continue
		}
		if connect.CodeOf(mapped) != tc.code {
			t.Fatalf("runConnectError(%v) code=%s want %s", tc.err, connect.CodeOf(mapped), tc.code)
		}
	}
}

func TestStopProjectSandboxCoverage(t *testing.T) {
	ctx := context.Background()
	if err := stopProjectSandbox(ctx, nil, nil, nil, nil); err != nil {
		t.Fatalf("stopProjectSandbox nil sandbox err=%v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-1"}}
	if err := stopProjectSandbox(ctx, nil, nil, nil, session); err == nil {
		t.Fatalf("stopProjectSandbox nil store returned nil error")
	}
	store := &projectSessionStoreFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-1", VMStatus: domain.VMStatusStopped}}}
	if err := stopProjectSandbox(ctx, store, nil, nil, session); err != nil || store.updated {
		t.Fatalf("stopProjectSandbox stopped err=%v updated=%v", err, store.updated)
	}
	store.session.Summary.VMStatus = domain.VMStatusRunning
	if err := stopProjectSandbox(ctx, store, nil, nil, session); err == nil {
		t.Fatalf("stopProjectSandbox nil driver returned nil error")
	}
	driver := &projectSandboxDriverFake{err: errors.New("stop failed")}
	if err := stopProjectSandbox(ctx, store, driver, nil, session); err == nil {
		t.Fatalf("stopProjectSandbox driver error returned nil error")
	}
	driver.err = nil
	streams := &projectSessionStreamsFake{}
	if err := stopProjectSandbox(ctx, store, driver, streams, session); err != nil {
		t.Fatalf("stopProjectSandbox running err=%v", err)
	}
	if !store.updated || store.session.Summary.VMStatus != domain.VMStatusStopped || len(store.events) != 1 || streams.updated == 0 || streams.events == 0 {
		t.Fatalf("stopProjectSandbox store=%#v streams=%#v", store, streams)
	}
}

func TestReserveLoaderEventQueueSlotsCoverage(t *testing.T) {
	config := &appconfig.Config{WebhookQueueDefaultWorkers: 1}
	var queue *webhooks.RunQueue
	if reservations, ok := reserveLoaderEventQueueSlots(config, &queue, domain.LoaderTopicEvent{Source: domain.TopicEventSourceWebhook}, 0); !ok || reservations != nil {
		t.Fatalf("zero reservations = %#v ok=%v", reservations, ok)
	}
	reservations, ok := reserveLoaderEventQueueSlots(config, &queue, domain.LoaderTopicEvent{Source: domain.TopicEventSourceLoader}, 2)
	if !ok || len(reservations) != 2 || queue != nil {
		t.Fatalf("non-webhook reservations = %#v ok=%v queue=%#v", reservations, ok, queue)
	}
	reservations, ok = reserveLoaderEventQueueSlots(config, &queue, domain.LoaderTopicEvent{Source: domain.TopicEventSourceWebhook, Topic: "runtime.topic"}, 1)
	if !ok || len(reservations) != 1 || queue == nil {
		t.Fatalf("webhook reservations = %#v ok=%v queue=%#v", reservations, ok, queue)
	}
	defer reservations[0].Release()
	if reservations, ok := reserveLoaderEventQueueSlots(config, &queue, domain.LoaderTopicEvent{Source: domain.TopicEventSourceWebhook, Topic: "runtime.topic"}, 1); ok || reservations != nil {
		t.Fatalf("saturated reservations = %#v ok=%v", reservations, ok)
	}

	var fallbackQueue *webhooks.RunQueue
	reservations, ok = reserveLoaderEventQueueSlots(&appconfig.Config{WebhookQueueRulesJSON: `bad`}, &fallbackQueue, domain.LoaderTopicEvent{Source: domain.TopicEventSourceWebhook}, 1)
	if !ok || len(reservations) != 1 || fallbackQueue == nil {
		t.Fatalf("fallback reservations = %#v ok=%v queue=%#v", reservations, ok, fallbackQueue)
	}
}

func TestIntegrationAppControllerHelperCoverage(t *testing.T) {
	TestAppProjectControllerHelperCoverage(t)
	TestAppRunControllerHelperCoverage(t)
	TestStopProjectSandboxCoverage(t)
	TestReserveLoaderEventQueueSlotsCoverage(t)
}

func TestE2EAppControllerHelperCoverage(t *testing.T) {
	TestIntegrationAppControllerHelperCoverage(t)
}

type projectSessionStoreFake struct {
	session *domain.Sandbox
	updated bool
	events  []domain.SandboxEvent
}

func (s *projectSessionStoreFake) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	if s.session == nil {
		return nil, sql.ErrNoRows
	}
	copy := *s.session
	return &copy, nil
}

func (s *projectSessionStoreFake) UpdateSandbox(_ context.Context, session *domain.Sandbox) error {
	s.updated = true
	copy := *session
	s.session = &copy
	return nil
}

func (s *projectSessionStoreFake) AddEvent(_ context.Context, _ string, event domain.SandboxEvent) error {
	s.events = append(s.events, event)
	return nil
}

type projectSandboxDriverFake struct {
	err error
}

func (d *projectSandboxDriverFake) StopSandboxVM(context.Context, *domain.Sandbox) error {
	return d.err
}

type projectSessionStreamsFake struct {
	updated int
	events  int
}

func (s *projectSessionStreamsFake) PublishSandboxUpdated(*domain.SandboxSummary) {
	s.updated++
}

func (s *projectSessionStreamsFake) PublishEventAdded(string, domain.SandboxEvent) {
	s.events++
}

var _ projectSandboxStore = (*projectSessionStoreFake)(nil)
var _ projectSandboxDriver = (*projectSandboxDriverFake)(nil)
var _ projectSandboxStreams = (*projectSessionStreamsFake)(nil)
