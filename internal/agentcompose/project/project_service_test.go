package project

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	appconfig "agent-compose/internal/config"
	driverpkg "agent-compose/internal/driver"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectServiceValidateProject(t *testing.T) {
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}

	validResp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
		Spec:   newProjectServiceTestSpec("demo", "gpt-test"),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ValidateProject valid returned error: %v", err)
	}
	if !validResp.Msg.GetValid() || validResp.Msg.GetSpecHash() == "" || len(validResp.Msg.GetIssues()) != 0 {
		t.Fatalf("ValidateProject valid response = %#v", validResp.Msg)
	}

	invalidResp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
		Spec:   newProjectServiceTestSpec("Bad Name", "gpt-test"),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ValidateProject invalid returned error: %v", err)
	}
	if invalidResp.Msg.GetValid() || len(invalidResp.Msg.GetIssues()) != 1 {
		t.Fatalf("ValidateProject invalid response = %#v", invalidResp.Msg)
	}
	if got := invalidResp.Msg.GetIssues()[0].GetPath(); got != "name" {
		t.Fatalf("ValidateProject invalid path = %q, want name", got)
	}

	mismatchResp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
		Spec:             newProjectServiceTestSpec("demo", "gpt-test"),
		Source:           source,
		ExpectedSpecHash: "sha256:wrong",
	}))
	if err != nil {
		t.Fatalf("ValidateProject hash mismatch returned error: %v", err)
	}
	if mismatchResp.Msg.GetValid() || mismatchResp.Msg.GetSpecHash() != validResp.Msg.GetSpecHash() || len(mismatchResp.Msg.GetIssues()) != 1 {
		t.Fatalf("ValidateProject hash mismatch response = %#v", mismatchResp.Msg)
	}
	if got := mismatchResp.Msg.GetIssues()[0].GetPath(); got != "expected_spec_hash" {
		t.Fatalf("ValidateProject hash mismatch path = %q, want expected_spec_hash", got)
	}
}

func TestProjectServiceValidateProjectAcceptsSchedulerScript(t *testing.T) {
	testProjectServiceValidateProjectAcceptsSchedulerScript(t)
}

func TestE2EProjectServiceValidateProjectAcceptsSchedulerScript(t *testing.T) {
	testProjectServiceValidateProjectAcceptsSchedulerScript(t)
}

func testProjectServiceValidateProjectAcceptsSchedulerScript(t *testing.T) {
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}
	firstSpec := newProjectServiceInlineSchedulerScriptSpec("demo", `scheduler.interval("hourly-review", function hourlyReview() {}, 3600000);`)
	secondSpec := newProjectServiceInlineSchedulerScriptSpec("demo", `scheduler.interval("daily-review", function dailyReview() {}, 86400000);`)

	firstResp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
		Spec:   firstSpec,
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ValidateProject first script returned error: %v", err)
	}
	if !firstResp.Msg.GetValid() || len(firstResp.Msg.GetIssues()) != 0 || firstResp.Msg.GetSpecHash() == "" {
		t.Fatalf("ValidateProject first script response = %#v", firstResp.Msg)
	}

	secondResp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
		Spec:   secondSpec,
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ValidateProject second script returned error: %v", err)
	}
	if !secondResp.Msg.GetValid() || len(secondResp.Msg.GetIssues()) != 0 || secondResp.Msg.GetSpecHash() == "" {
		t.Fatalf("ValidateProject second script response = %#v", secondResp.Msg)
	}
	if firstResp.Msg.GetSpecHash() == secondResp.Msg.GetSpecHash() {
		t.Fatalf("ValidateProject spec hash did not change when scheduler script changed: %s", firstResp.Msg.GetSpecHash())
	}
}

func TestProjectServiceValidateProjectSchedulerScriptRequiresLoaderManager(t *testing.T) {
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	service.loaders = nil
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}

	resp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
		Spec:   newProjectServiceInlineSchedulerScriptSpec("demo", `scheduler.interval("hourly-review", function hourlyReview() {}, 3600000);`),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ValidateProject returned error: %v", err)
	}
	if resp.Msg.GetValid() || len(resp.Msg.GetIssues()) != 1 {
		t.Fatalf("ValidateProject response = %#v, want one validation issue", resp.Msg)
	}
	issue := resp.Msg.GetIssues()[0]
	if issue.GetPath() != "agents.reviewer.scheduler.script" || !strings.Contains(issue.GetMessage(), "loader manager") {
		t.Fatalf("ValidateProject issue = %#v, want loader manager scheduler script issue", issue)
	}
}

func TestProjectServiceValidateProjectReportsSchedulerScriptValidationIssues(t *testing.T) {
	testProjectServiceValidateProjectReportsSchedulerScriptValidationIssues(t)
}

func TestE2EProjectServiceValidateProjectReportsSchedulerScriptValidationIssues(t *testing.T) {
	testProjectServiceValidateProjectReportsSchedulerScriptValidationIssues(t)
}

func testProjectServiceValidateProjectReportsSchedulerScriptValidationIssues(t *testing.T) {
	tests := []struct {
		name        string
		script      string
		wantMessage string
	}{
		{
			name:   "syntax",
			script: `const broken = ;`,
		},
		{
			name: "duplicate trigger id",
			script: `
scheduler.interval("duplicate", function first() {}, 1000);
scheduler.timeout("duplicate", function second() {}, 2000);
`,
			wantMessage: "duplicate loader trigger id",
		},
		{
			name:        "invalid timeout",
			script:      `scheduler.timeout(function timeout() {}, 0);`,
			wantMessage: "positive delay",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newProjectServiceTestService(t, newTestConfigStore(t))
			ctx := context.Background()
			source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}

			resp, err := service.ValidateProject(ctx, connect.NewRequest(&agentcomposev2.ValidateProjectRequest{
				Spec:   newProjectServiceInlineSchedulerScriptSpec("demo", tt.script),
				Source: source,
			}))
			if err != nil {
				t.Fatalf("ValidateProject returned error: %v", err)
			}
			if resp.Msg.GetValid() || len(resp.Msg.GetIssues()) != 1 {
				t.Fatalf("ValidateProject response = %#v, want one validation issue", resp.Msg)
			}
			issue := resp.Msg.GetIssues()[0]
			if issue.GetPath() != "agents.reviewer.scheduler.script" {
				t.Fatalf("ValidateProject issue path = %q, want scheduler script", issue.GetPath())
			}
			if strings.TrimSpace(issue.GetMessage()) == "" {
				t.Fatalf("ValidateProject issue message is empty: %#v", issue)
			}
			if tt.wantMessage != "" && !strings.Contains(issue.GetMessage(), tt.wantMessage) {
				t.Fatalf("ValidateProject issue message = %q, want %q", issue.GetMessage(), tt.wantMessage)
			}
		})
	}
}

func TestProjectSpecResponseIncludesSchedulerScript(t *testing.T) {
	const script = `scheduler.interval("hourly-review", "1h");`
	spec := &compose.NormalizedProjectSpec{
		Name:    "inline-script",
		Network: &compose.NetworkSpec{Mode: "default"},
		Agents: []compose.NormalizedAgentSpec{{
			Name: "reviewer",
			Driver: &compose.NormalizedDriverSpec{
				Name:    compose.DriverBoxlite,
				Boxlite: &compose.BoxliteDriverSpec{},
			},
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled: true,
				Script:  script,
			},
		}},
	}

	response := ProjectSpecResponse(spec)
	if response == nil || len(response.GetAgents()) != 1 || response.GetAgents()[0].GetScheduler() == nil {
		t.Fatalf("ProjectSpecResponse scheduler missing: %#v", response)
	}
	scheduler := response.GetAgents()[0].GetScheduler()
	if scheduler.GetScript() != script {
		t.Fatalf("scheduler script = %q, want %q", scheduler.GetScript(), script)
	}
	if got := len(scheduler.GetTriggers()); got != 0 {
		t.Fatalf("scheduler triggers = %d, want 0", got)
	}
}

func TestProjectServiceApplyProjectInlineSchedulerMainOnlyAllowsZeroTriggers(t *testing.T) {
	testProjectServiceApplyProjectInlineSchedulerMainOnlyAllowsZeroTriggers(t)
}

func TestE2EProjectServiceApplyProjectInlineSchedulerMainOnlyAllowsZeroTriggers(t *testing.T) {
	testProjectServiceApplyProjectInlineSchedulerMainOnlyAllowsZeroTriggers(t)
}

func testProjectServiceApplyProjectInlineSchedulerMainOnlyAllowsZeroTriggers(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	projectDir := t.TempDir()
	script := `function main(payload) { return payload; }`

	resp, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceInlineSchedulerScriptSpec("demo", script),
		Source: &agentcomposev2.ProjectSource{ComposePath: filepath.Join(projectDir, "agent-compose.yml")},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if !resp.Msg.GetApplied() || len(resp.Msg.GetIssues()) != 0 {
		t.Fatalf("ApplyProject response = %#v", resp.Msg)
	}
	project := resp.Msg.GetProject()
	if len(project.GetSchedulers()) != 1 || project.GetSchedulers()[0].GetTriggerCount() != 0 {
		t.Fatalf("project schedulers = %#v, want one scheduler with zero triggers", project.GetSchedulers())
	}
	loaderID := managedLoaderIDByAgentName(t, project.GetSchedulers(), "reviewer")
	loader, err := store.GetLoader(ctx, loaderID)
	if err != nil {
		t.Fatalf("GetLoader(%s) returned error: %v", loaderID, err)
	}
	if loader.Script != script {
		t.Fatalf("managed loader script = %q, want %q", loader.Script, script)
	}
	if got := len(loader.Triggers); got != 0 {
		t.Fatalf("managed loader triggers = %d, want 0", got)
	}
}

func TestProjectServiceApplyProjectInlineSchedulerRevisionLifecycle(t *testing.T) {
	testProjectServiceApplyProjectInlineSchedulerRevisionLifecycle(t)
}

func TestE2EProjectServiceApplyProjectInlineSchedulerRevisionLifecycle(t *testing.T) {
	testProjectServiceApplyProjectInlineSchedulerRevisionLifecycle(t)
}

func testProjectServiceApplyProjectInlineSchedulerRevisionLifecycle(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}
	firstScript := `scheduler.interval("interval-review", function intervalReview() {}, 60000);`
	changedScript := `scheduler.interval("interval-review", function intervalReview() {}, 120000);`

	first, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceInlineSchedulerScriptSpec("inline-demo", firstScript),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject first returned error: %v", err)
	}
	if !first.Msg.GetApplied() || first.Msg.GetUnchanged() || first.Msg.GetRevision().GetRevision() != 1 {
		t.Fatalf("ApplyProject first response = %#v", first.Msg)
	}
	project := first.Msg.GetProject()
	projectID := project.GetSummary().GetProjectId()
	schedulerID := managedSchedulerIDByAgentName(t, project.GetSchedulers(), "reviewer")
	loaderID := managedLoaderIDByAgentName(t, project.GetSchedulers(), "reviewer")
	if project.GetSchedulers()[0].GetTriggerCount() != 1 {
		t.Fatalf("first scheduler trigger count = %d, want 1", project.GetSchedulers()[0].GetTriggerCount())
	}
	firstLoader := assertManagedLoader(t, store, ctx, loaderID, projectID, "reviewer", schedulerID, 1, true)
	if firstLoader.Script != firstScript || len(firstLoader.Triggers) != 1 || firstLoader.Triggers[0].IntervalMs != 60000 {
		t.Fatalf("first managed loader = script %q triggers %#v", firstLoader.Script, firstLoader.Triggers)
	}

	repeated, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceInlineSchedulerScriptSpec("inline-demo", firstScript),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject repeated returned error: %v", err)
	}
	if !repeated.Msg.GetApplied() || !repeated.Msg.GetUnchanged() || repeated.Msg.GetRevision().GetRevision() != 1 {
		t.Fatalf("ApplyProject repeated response = %#v", repeated.Msg)
	}
	if repeatedLoaderID := managedLoaderIDByAgentName(t, repeated.Msg.GetProject().GetSchedulers(), "reviewer"); repeatedLoaderID != loaderID {
		t.Fatalf("managed loader id changed after repeated apply: %q != %q", repeatedLoaderID, loaderID)
	}

	changed, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceInlineSchedulerScriptSpec("inline-demo", changedScript),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject changed returned error: %v", err)
	}
	if !changed.Msg.GetApplied() || changed.Msg.GetUnchanged() || changed.Msg.GetRevision().GetRevision() != 2 {
		t.Fatalf("ApplyProject changed response = %#v", changed.Msg)
	}
	if changedLoaderID := managedLoaderIDByAgentName(t, changed.Msg.GetProject().GetSchedulers(), "reviewer"); changedLoaderID != loaderID {
		t.Fatalf("managed loader id changed after script update: %q != %q", changedLoaderID, loaderID)
	}
	changedLoader := assertManagedLoader(t, store, ctx, loaderID, projectID, "reviewer", schedulerID, 2, true)
	if changedLoader.Script != changedScript || len(changedLoader.Triggers) != 1 || changedLoader.Triggers[0].IntervalMs != 120000 {
		t.Fatalf("changed managed loader = script %q triggers %#v", changedLoader.Script, changedLoader.Triggers)
	}
	changedScheduler, err := store.GetProjectScheduler(ctx, projectID, schedulerID)
	if err != nil {
		t.Fatalf("GetProjectScheduler changed returned error: %v", err)
	}
	if changedScheduler.TriggerCount != 1 || changedScheduler.Revision != 2 || !changedScheduler.Enabled {
		t.Fatalf("changed scheduler = %#v, want revision 2 enabled with one trigger", changedScheduler)
	}

	withoutSchedulerSpec := newProjectServiceInlineSchedulerScriptSpec("inline-demo", changedScript)
	withoutSchedulerSpec.Agents[0].Scheduler = nil
	withoutScheduler, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   withoutSchedulerSpec,
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject without scheduler returned error: %v", err)
	}
	if !withoutScheduler.Msg.GetApplied() || withoutScheduler.Msg.GetUnchanged() || withoutScheduler.Msg.GetRevision().GetRevision() != 3 {
		t.Fatalf("ApplyProject without scheduler response = %#v", withoutScheduler.Msg)
	}
	disabledScheduler, err := store.GetProjectScheduler(ctx, projectID, schedulerID)
	if err != nil {
		t.Fatalf("GetProjectScheduler disabled returned error: %v", err)
	}
	if disabledScheduler.Enabled {
		t.Fatalf("removed scheduler stayed enabled: %#v", disabledScheduler)
	}
	disabledLoader := assertManagedLoader(t, store, ctx, loaderID, projectID, "reviewer", schedulerID, 2, false)
	if disabledLoader.Script != changedScript {
		t.Fatalf("disabled managed loader script = %q, want changed script", disabledLoader.Script)
	}
	for _, trigger := range disabledLoader.Triggers {
		if !trigger.NextFireAt.IsZero() {
			t.Fatalf("disabled loader trigger kept next fire time: %#v", trigger)
		}
	}
}

func TestProjectServiceApplyProjectPersistsAgentCapsetIDs(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}
	spec := newProjectServiceTestSpec("capset-demo", "gpt-test")
	spec.Agents[0].CapsetIds = []string{"xray-dev", "xray-dev", " data "}

	resp, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   spec,
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if !resp.Msg.GetApplied() {
		t.Fatalf("ApplyProject response = %#v", resp.Msg)
	}
	project := resp.Msg.GetProject()
	projectID := project.GetSummary().GetProjectId()
	reviewerManagedID := managedAgentIDByName(t, project.GetAgents(), "reviewer")
	reviewerDefinition := assertManagedAgentDefinition(t, store, ctx, reviewerManagedID, projectID, "reviewer", 1, true)
	assertStringSliceEqual(t, reviewerDefinition.CapsetIDs, []string{"xray-dev", "data"}, "managed reviewer capset ids")

	managedSchedulerID := managedSchedulerIDByAgentName(t, project.GetSchedulers(), "reviewer")
	managedLoaderID := managedLoaderIDByAgentName(t, project.GetSchedulers(), "reviewer")
	reviewerLoader := assertManagedLoader(t, store, ctx, managedLoaderID, projectID, "reviewer", managedSchedulerID, 1, true)
	assertStringSliceEqual(t, reviewerLoader.Summary.CapsetIDs, []string{"xray-dev", "data"}, "managed reviewer loader capset ids")

	loaded, err := service.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
		IncludeSpec: true,
	}))
	if err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}
	var reviewerSpec *agentcomposev2.AgentSpec
	for _, agent := range loaded.Msg.GetProject().GetSpec().GetAgents() {
		if agent.GetName() == "reviewer" {
			reviewerSpec = agent
			break
		}
	}
	if reviewerSpec == nil {
		t.Fatalf("reviewer spec missing from GetProject response: %#v", loaded.Msg.GetProject().GetSpec())
	}
	assertStringSliceEqual(t, reviewerSpec.GetCapsetIds(), []string{"xray-dev", "data"}, "GetProject reviewer spec capset ids")
}

func TestProjectServiceGetProjectAndListProjects(t *testing.T) {
	testProjectServiceGetProjectAndListProjects(t)
}

func TestE2EProjectServiceGetProjectAndListProjects(t *testing.T) {
	testProjectServiceGetProjectAndListProjects(t)
}

func testProjectServiceGetProjectAndListProjects(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	projectDir := t.TempDir()
	composePath := filepath.Join(projectDir, "agent-compose.yml")
	applyResp, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceTestSpec("demo", "gpt-test"),
		Source: &agentcomposev2.ProjectSource{ComposePath: composePath},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	projectID := applyResp.Msg.GetProject().GetSummary().GetProjectId()

	getResp, err := service.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     &agentcomposev2.ProjectRef{Name: "demo", SourcePath: composePath},
		IncludeSpec: true,
	}))
	if err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}
	if getResp.Msg.GetProject().GetSummary().GetProjectId() != projectID {
		t.Fatalf("GetProject id = %q, want %q", getResp.Msg.GetProject().GetSummary().GetProjectId(), projectID)
	}
	if len(getResp.Msg.GetProject().GetAgents()) != 2 || len(getResp.Msg.GetProject().GetSchedulers()) != 1 {
		t.Fatalf("GetProject resources = agents %d schedulers %d", len(getResp.Msg.GetProject().GetAgents()), len(getResp.Msg.GetProject().GetSchedulers()))
	}
	if getResp.Msg.GetProject().GetSpec().GetName() != "demo" {
		t.Fatalf("GetProject spec name = %q, want demo", getResp.Msg.GetProject().GetSpec().GetName())
	}

	listResp, err := service.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Query: "demo"}))
	if err != nil {
		t.Fatalf("ListProjects returned error: %v", err)
	}
	if listResp.Msg.GetTotalCount() != 1 || len(listResp.Msg.GetProjects()) != 1 || listResp.Msg.GetProjects()[0].GetProjectId() != projectID {
		t.Fatalf("ListProjects response = %#v", listResp.Msg)
	}
}

func TestProjectServiceManagedSchedulerCompileCoversTriggerKinds(t *testing.T) {
	testProjectServiceManagedSchedulerCompileCoversTriggerKinds(t)
}

func TestIntegrationProjectServiceManagedSchedulerCompileCoversTriggerKinds(t *testing.T) {
	testProjectServiceManagedSchedulerCompileCoversTriggerKinds(t)
}

func TestE2EProjectServiceManagedSchedulerCompileCoversTriggerKinds(t *testing.T) {
	testProjectServiceManagedSchedulerCompileCoversTriggerKinds(t)
}

func TestProjectServiceManagedSchedulerBuildUsesInlineScript(t *testing.T) {
	testProjectServiceManagedSchedulerBuildUsesInlineScript(t)
}

func TestE2EProjectServiceManagedSchedulerBuildUsesInlineScript(t *testing.T) {
	testProjectServiceManagedSchedulerBuildUsesInlineScript(t)
}

func testProjectServiceManagedSchedulerBuildUsesInlineScript(t *testing.T) {
	const script = `scheduler.interval("hourly-review", function hourlyReview() {}, 3600000);`
	spec := &compose.NormalizedProjectSpec{
		Name: "inline-build",
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "codex",
			Image:    "guest:v1",
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled: true,
				Script:  script,
			},
		}},
	}
	project := ProjectRecord{ID: "project-demo", Name: "demo"}

	builds, err := projectManagedSchedulerBuildsFromSpec(project, 7, spec)
	if err != nil {
		t.Fatalf("projectManagedSchedulerBuildsFromSpec returned error: %v", err)
	}
	if len(builds) != 1 {
		t.Fatalf("builds = %#v, want one scheduler build", builds)
	}
	build := builds[0]
	if build.loader.Script != script {
		t.Fatalf("managed loader script = %q, want user script %q", build.loader.Script, script)
	}
	if got := len(build.loader.Triggers); got != 0 {
		t.Fatalf("managed loader triggers = %d, want 0 before validation trigger persistence", got)
	}
	if got := len(build.validationTriggers); got != 0 {
		t.Fatalf("validation triggers = %d, want 0 before validation trigger persistence", got)
	}
	if build.scheduler.TriggerCount != 0 {
		t.Fatalf("scheduler trigger count = %d, want 0 before validation trigger persistence", build.scheduler.TriggerCount)
	}
	if build.scheduler.Revision != 7 || !build.scheduler.Enabled {
		t.Fatalf("scheduler record = %#v, want revision 7 enabled", build.scheduler)
	}
}

func TestProjectServiceManagedSchedulerBuildUsesInlineValidationTriggers(t *testing.T) {
	testProjectServiceManagedSchedulerBuildUsesInlineValidationTriggers(t)
}

func TestE2EProjectServiceManagedSchedulerBuildUsesInlineValidationTriggers(t *testing.T) {
	testProjectServiceManagedSchedulerBuildUsesInlineValidationTriggers(t)
}

func testProjectServiceManagedSchedulerBuildUsesInlineValidationTriggers(t *testing.T) {
	script := strings.TrimSpace(`
scheduler.interval("interval-review", function intervalReview() {}, 60000);
scheduler.timeout("timeout-review", function timeoutReview() {}, 5000);
scheduler.cron("*/15 * * * *", function cronReview() {}, { id: "cron-review", timezone: "UTC" });
scheduler.on("agent-compose.session.created", function onSession() {});
`)
	spec := &compose.NormalizedProjectSpec{
		Name: "inline-validation",
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "codex",
			Image:    "guest:v1",
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled: true,
				Script:  script,
			},
		}},
	}
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	project := ProjectRecord{ID: "project-demo", Name: "demo"}

	builds, err := service.projectManagedSchedulerBuildsFromSpec(context.Background(), project, 7, spec)
	if err != nil {
		t.Fatalf("projectManagedSchedulerBuildsFromSpec returned error: %v", err)
	}
	if len(builds) != 1 {
		t.Fatalf("builds = %#v, want one scheduler build", builds)
	}
	build := builds[0]
	if build.loader.Script != script {
		t.Fatalf("managed loader script = %q, want user script", build.loader.Script)
	}
	if build.scheduler.TriggerCount != 4 {
		t.Fatalf("scheduler trigger count = %d, want 4", build.scheduler.TriggerCount)
	}
	if len(build.loader.Triggers) != 4 || len(build.validationTriggers) != 4 {
		t.Fatalf("loader/validation triggers = %#v / %#v, want 4 triggers", build.loader.Triggers, build.validationTriggers)
	}
	triggers := build.loader.Triggers
	if triggers[0].Kind != LoaderTriggerKindInterval || triggers[0].ID != "interval-review" || triggers[0].IntervalMs != 60000 {
		t.Fatalf("interval trigger = %#v", triggers[0])
	}
	if triggers[1].Kind != LoaderTriggerKindTimeout || triggers[1].ID != "timeout-review" || triggers[1].IntervalMs != 5000 {
		t.Fatalf("timeout trigger = %#v", triggers[1])
	}
	if triggers[2].Kind != LoaderTriggerKindCron || triggers[2].ID != "cron-review" || !strings.Contains(triggers[2].SpecJSON, `"timezone":"UTC"`) {
		t.Fatalf("cron trigger = %#v", triggers[2])
	}
	if triggers[3].Kind != LoaderTriggerKindEvent || triggers[3].Topic != "agent-compose.session.created" {
		t.Fatalf("event trigger = %#v", triggers[3])
	}
}

func testProjectServiceManagedSchedulerCompileCoversTriggerKinds(t *testing.T) {
	t.Helper()
	scheduler := &compose.NormalizedSchedulerSpec{
		Enabled: true,
		Triggers: []compose.NormalizedTriggerSpec{
			{Name: "hourly", Kind: "cron", Cron: "0 * * * *", Prompt: "review hourly"},
			{Name: "pulse", Kind: "interval", Interval: "1m30s", Prompt: "review interval"},
			{Kind: "timeout", Timeout: "5s", Prompt: "review once"},
			{Name: "push", Kind: "event", Event: &compose.EventTriggerSpec{Topic: "git.push"}, Prompt: "review push"},
		},
	}
	triggers, script, err := projectManagedLoaderTriggersAndScript("project-demo", "reviewer", "", scheduler)
	if err != nil {
		t.Fatalf("projectManagedLoaderTriggersAndScript returned error: %v", err)
	}
	if len(triggers) != 4 {
		t.Fatalf("trigger count = %d, want 4: %#v", len(triggers), triggers)
	}
	again, againScript, err := projectManagedLoaderTriggersAndScript("project-demo", "reviewer", "", scheduler)
	if err != nil {
		t.Fatalf("repeat compile returned error: %v", err)
	}
	if !sameLoaderTriggerSpecs(triggers, again) || script != againScript {
		t.Fatalf("compiled scheduler is not stable:\n%#v\n%#v\n%s\n%s", triggers, again, script, againScript)
	}
	byKind := map[string]LoaderTrigger{}
	for _, trigger := range triggers {
		if strings.TrimSpace(trigger.ID) == "" {
			t.Fatalf("trigger has empty id: %#v", trigger)
		}
		byKind[trigger.Kind] = trigger
		if !strings.Contains(script, trigger.ID) {
			t.Fatalf("script does not contain trigger id %q:\n%s", trigger.ID, script)
		}
	}
	if got := byKind[LoaderTriggerKindCron].SpecJSON; !strings.Contains(got, `"expr":"0 * * * *"`) || !strings.Contains(got, `"timezone":"UTC"`) {
		t.Fatalf("cron spec = %q", got)
	}
	if got := byKind[LoaderTriggerKindInterval].IntervalMs; got != 90_000 {
		t.Fatalf("interval ms = %d, want 90000", got)
	}
	if got := byKind[LoaderTriggerKindTimeout].IntervalMs; got != 5_000 {
		t.Fatalf("timeout ms = %d, want 5000", got)
	}
	if got := byKind[LoaderTriggerKindEvent].Topic; got != "git.push" {
		t.Fatalf("event topic = %q, want git.push", got)
	}
	if !strings.Contains(script, "scheduler.agent") || !strings.Contains(script, "review hourly") || !strings.Contains(script, "review push") {
		t.Fatalf("script does not call scheduler.agent with prompts:\n%s", script)
	}

	duplicate := &compose.NormalizedSchedulerSpec{
		Enabled: true,
		Triggers: []compose.NormalizedTriggerSpec{
			{Name: "same", Kind: "interval", Interval: "1s"},
			{Name: "same", Kind: "timeout", Timeout: "2s"},
		},
	}
	if _, _, err := projectManagedLoaderTriggersAndScript("project-demo", "reviewer", "", duplicate); err == nil || !strings.Contains(err.Error(), "duplicate scheduler trigger name") {
		t.Fatalf("duplicate trigger name error = %v", err)
	}
}

func TestIntegrationProjectServiceApplyProjectCreatesAndReusesRevision(t *testing.T) {
	testProjectServiceApplyProjectCreatesAndReusesRevision(t)
}

func TestE2EProjectServiceApplyProjectCreatesAndReusesRevision(t *testing.T) {
	testProjectServiceApplyProjectCreatesAndReusesRevision(t)
}

func testProjectServiceApplyProjectCreatesAndReusesRevision(t *testing.T) {
	t.Helper()
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}
	manualAgent, err := store.CreateAgentDefinition(ctx, AgentDefinition{
		ID:       "manual-reviewer",
		Name:     "reviewer",
		Enabled:  true,
		Provider: "gemini",
		Model:    "manual-model",
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition manual returned error: %v", err)
	}
	manualLoader, err := store.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{
			ID:      "manual-reviewer-loader",
			Name:    "reviewer scheduler",
			Enabled: true,
			Runtime: LoaderRuntimeScheduler,
		},
		Script: "function main() { return { manual: true }; }",
	})
	if err != nil {
		t.Fatalf("CreateLoader manual returned error: %v", err)
	}

	first, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceTestSpec("demo", "gpt-test"),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject first returned error: %v", err)
	}
	if !first.Msg.GetApplied() || first.Msg.GetUnchanged() {
		t.Fatalf("ApplyProject first applied/unchanged = %v/%v", first.Msg.GetApplied(), first.Msg.GetUnchanged())
	}
	project := first.Msg.GetProject()
	if project.GetSummary().GetProjectId() == "" || project.GetSummary().GetCurrentRevision() != 1 || first.Msg.GetRevision().GetRevision() != 1 {
		t.Fatalf("ApplyProject first project/revision = %#v / %#v", project.GetSummary(), first.Msg.GetRevision())
	}
	if len(project.GetAgents()) != 2 || project.GetSummary().GetAgentCount() != 2 || project.GetSummary().GetSchedulerCount() != 1 {
		t.Fatalf("ApplyProject first project = %#v", project)
	}
	assertProjectServiceAgent(t, project.GetAgents(), "reviewer", true, "gpt-test")
	assertProjectServiceAgent(t, project.GetAgents(), "worker", false, "")

	projectID := project.GetSummary().GetProjectId()
	manualAfterFirst, err := store.GetAgentDefinition(ctx, manualAgent.ID)
	if err != nil {
		t.Fatalf("GetAgentDefinition manual after first apply returned error: %v", err)
	}
	if manualAfterFirst.Provider != "gemini" || manualAfterFirst.Model != "manual-model" || manualAfterFirst.ManagedProjectID != "" {
		t.Fatalf("manual agent after first apply = %#v", manualAfterFirst)
	}
	reviewerManagedID := managedAgentIDByName(t, project.GetAgents(), "reviewer")
	workerManagedID := managedAgentIDByName(t, project.GetAgents(), "worker")
	if reviewerManagedID == manualAgent.ID {
		t.Fatalf("managed reviewer reused manual agent id %q", reviewerManagedID)
	}
	reviewerDefinition := assertManagedAgentDefinition(t, store, ctx, reviewerManagedID, projectID, "reviewer", 1, true)
	if reviewerDefinition.Provider != "codex" || reviewerDefinition.Model != "gpt-test" || reviewerDefinition.GuestImage != "guest:v1" || reviewerDefinition.Driver != "boxlite" {
		t.Fatalf("managed reviewer definition = %#v", reviewerDefinition)
	}
	workerDefinition := assertManagedAgentDefinition(t, store, ctx, workerManagedID, projectID, "worker", 1, true)
	if workerDefinition.Provider != "claude" || workerDefinition.Driver != "docker" {
		t.Fatalf("managed worker definition = %#v", workerDefinition)
	}
	managedSchedulerID := managedSchedulerIDByAgentName(t, project.GetSchedulers(), "reviewer")
	managedLoaderID := managedLoaderIDByAgentName(t, project.GetSchedulers(), "reviewer")
	if managedLoaderID == manualLoader.Summary.ID {
		t.Fatalf("managed loader reused manual loader id %q", managedLoaderID)
	}
	reviewerLoader := assertManagedLoader(t, store, ctx, managedLoaderID, projectID, "reviewer", managedSchedulerID, 1, true)
	if reviewerLoader.Summary.AgentID != reviewerManagedID || reviewerLoader.Summary.Driver != "boxlite" || reviewerLoader.Summary.GuestImage != "guest:v1" {
		t.Fatalf("managed reviewer loader = %#v", reviewerLoader.Summary)
	}
	if len(reviewerLoader.Triggers) != 1 || reviewerLoader.Triggers[0].Kind != LoaderTriggerKindCron || !strings.Contains(reviewerLoader.Triggers[0].SpecJSON, `"expr":"0 * * * *"`) {
		t.Fatalf("managed reviewer loader triggers = %#v", reviewerLoader.Triggers)
	}
	loaded, err := store.GetProject(ctx, projectID)
	if err != nil {
		t.Fatalf("GetProject after apply returned error: %v", err)
	}
	if loaded.CurrentRevision != 1 || loaded.SpecHash != first.Msg.GetRevision().GetSpecHash() {
		t.Fatalf("loaded project = %#v, want revision/hash from response", loaded)
	}
	agents, err := store.ListProjectAgents(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectAgents after apply returned error: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("project agent count = %d, want 2", len(agents))
	}
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers after apply returned error: %v", err)
	}
	if len(schedulers) != 1 || schedulers[0].ManagedLoaderID != managedLoaderID || schedulers[0].TriggerCount != 1 {
		t.Fatalf("project schedulers = %#v, want managed reviewer scheduler", schedulers)
	}
	manualLoaderAfterFirst, err := store.GetLoader(ctx, manualLoader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader manual after first apply returned error: %v", err)
	}
	if manualLoaderAfterFirst.Summary.ManagedProjectID != "" || manualLoaderAfterFirst.Script != manualLoader.Script {
		t.Fatalf("manual loader after first apply = %#v", manualLoaderAfterFirst)
	}

	repeated, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceTestSpec("demo", "gpt-test"),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject repeated returned error: %v", err)
	}
	if !repeated.Msg.GetApplied() || !repeated.Msg.GetUnchanged() || repeated.Msg.GetRevision().GetRevision() != 1 {
		t.Fatalf("ApplyProject repeated response = %#v", repeated.Msg)
	}
	repeatedReviewerID := managedAgentIDByName(t, repeated.Msg.GetProject().GetAgents(), "reviewer")
	if repeatedReviewerID != reviewerManagedID {
		t.Fatalf("managed reviewer id changed after repeated apply: %q != %q", repeatedReviewerID, reviewerManagedID)
	}
	if repeatedLoaderID := managedLoaderIDByAgentName(t, repeated.Msg.GetProject().GetSchedulers(), "reviewer"); repeatedLoaderID != managedLoaderID {
		t.Fatalf("managed loader id changed after repeated apply: %q != %q", repeatedLoaderID, managedLoaderID)
	}

	changed, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceTestSpec("demo", "gpt-next"),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject changed returned error: %v", err)
	}
	if !changed.Msg.GetApplied() || changed.Msg.GetUnchanged() || changed.Msg.GetRevision().GetRevision() != 2 {
		t.Fatalf("ApplyProject changed response = %#v", changed.Msg)
	}
	assertProjectServiceAgent(t, changed.Msg.GetProject().GetAgents(), "reviewer", true, "gpt-next")
	changedReviewer := assertManagedAgentDefinition(t, store, ctx, reviewerManagedID, projectID, "reviewer", 2, true)
	if changedReviewer.Model != "gpt-next" {
		t.Fatalf("changed managed reviewer model = %q, want gpt-next", changedReviewer.Model)
	}
	changedLoader := assertManagedLoader(t, store, ctx, managedLoaderID, projectID, "reviewer", managedSchedulerID, 2, true)
	if changedLoader.Summary.AgentID != reviewerManagedID {
		t.Fatalf("changed managed loader agent id = %q, want %q", changedLoader.Summary.AgentID, reviewerManagedID)
	}
	manualAfterChanged, err := store.GetAgentDefinition(ctx, manualAgent.ID)
	if err != nil {
		t.Fatalf("GetAgentDefinition manual after changed apply returned error: %v", err)
	}
	if manualAfterChanged.Provider != "gemini" || manualAfterChanged.Model != "manual-model" || !manualAfterChanged.Enabled {
		t.Fatalf("manual agent after changed apply = %#v", manualAfterChanged)
	}

	withoutSchedulerSpec := newProjectServiceTestSpec("demo", "gpt-next")
	withoutSchedulerSpec.Agents[0].Scheduler = nil
	withoutScheduler, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   withoutSchedulerSpec,
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject without scheduler returned error: %v", err)
	}
	if !withoutScheduler.Msg.GetApplied() || withoutScheduler.Msg.GetUnchanged() || withoutScheduler.Msg.GetRevision().GetRevision() != 3 {
		t.Fatalf("ApplyProject without scheduler response = %#v", withoutScheduler.Msg)
	}
	disabledScheduler, err := store.GetProjectScheduler(ctx, projectID, managedSchedulerID)
	if err != nil {
		t.Fatalf("GetProjectScheduler disabled returned error: %v", err)
	}
	if disabledScheduler.Enabled {
		t.Fatalf("removed scheduler stayed enabled: %#v", disabledScheduler)
	}
	disabledLoader := assertManagedLoader(t, store, ctx, managedLoaderID, projectID, "reviewer", managedSchedulerID, 2, false)
	for _, trigger := range disabledLoader.Triggers {
		if trigger.NextFireAt.IsZero() {
			continue
		}
		t.Fatalf("disabled loader trigger kept next fire time: %#v", trigger)
	}
	runsAfterUp, err := store.ListProjectRuns(ctx, projectID, 10)
	if err != nil {
		t.Fatalf("ListProjectRuns after scheduler removal returned error: %v", err)
	}
	if len(runsAfterUp) != 0 {
		t.Fatalf("ApplyProject created project runs: %#v", runsAfterUp)
	}

	readded, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   newProjectServiceTestSpec("demo", "gpt-next"),
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject readded scheduler returned error: %v", err)
	}
	if !readded.Msg.GetApplied() || readded.Msg.GetUnchanged() || readded.Msg.GetRevision().GetRevision() != 2 {
		t.Fatalf("ApplyProject readded scheduler response = %#v", readded.Msg)
	}
	if readdedSchedulerID := managedSchedulerIDByAgentName(t, readded.Msg.GetProject().GetSchedulers(), "reviewer"); readdedSchedulerID != managedSchedulerID {
		t.Fatalf("managed scheduler id changed after re-add: %q != %q", readdedSchedulerID, managedSchedulerID)
	}
	if readdedLoaderID := managedLoaderIDByAgentName(t, readded.Msg.GetProject().GetSchedulers(), "reviewer"); readdedLoaderID != managedLoaderID {
		t.Fatalf("managed loader id changed after re-add: %q != %q", readdedLoaderID, managedLoaderID)
	}
	assertManagedLoader(t, store, ctx, managedLoaderID, projectID, "reviewer", managedSchedulerID, 2, true)

	removedWorkerSpec := newProjectServiceTestSpec("demo", "gpt-next")
	removedWorkerSpec.Agents = removedWorkerSpec.Agents[:1]
	removed, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   removedWorkerSpec,
		Source: source,
	}))
	if err != nil {
		t.Fatalf("ApplyProject removed worker returned error: %v", err)
	}
	if !removed.Msg.GetApplied() || removed.Msg.GetUnchanged() || removed.Msg.GetRevision().GetRevision() != 4 {
		t.Fatalf("ApplyProject removed worker response = %#v", removed.Msg)
	}
	assertManagedAgentDefinition(t, store, ctx, reviewerManagedID, projectID, "reviewer", 4, true)
	assertManagedLoader(t, store, ctx, managedLoaderID, projectID, "reviewer", managedSchedulerID, 4, true)
	disabledWorker := assertManagedAgentDefinition(t, store, ctx, workerManagedID, projectID, "worker", 2, false)
	if !disabledWorker.DeletedAt.IsZero() {
		t.Fatalf("removed worker was hard-deleted: %#v", disabledWorker)
	}
}

func TestProjectServiceReconcileSchedulersFailureDisablesStagedResources(t *testing.T) {
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	project := ProjectRecord{ID: "project-demo", Name: "demo"}
	if _, err := store.UpsertProject(ctx, project); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, ProjectAgentRecord{
		ProjectID:      project.ID,
		AgentName:      "reviewer",
		ManagedAgentID: "agent-demo",
		Revision:       1,
		Provider:       "codex",
		SpecJSON:       `{}`,
	}); err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	scheduler := ProjectSchedulerRecord{
		ProjectID:       project.ID,
		SchedulerID:     "scheduler-demo",
		AgentName:       "reviewer",
		ManagedLoaderID: "loader-demo",
		Revision:        1,
		Enabled:         true,
		TriggerCount:    2,
		SpecJSON:        `{"enabled":true}`,
	}
	loader := Loader{
		Summary: LoaderSummary{
			ID:                 scheduler.ManagedLoaderID,
			Name:               "demo/reviewer scheduler",
			Enabled:            true,
			Runtime:            LoaderRuntimeScheduler,
			DefaultAgent:       "codex",
			SessionPolicy:      LoaderSessionPolicyNew,
			ConcurrencyPolicy:  LoaderConcurrencyPolicySkip,
			ManagedProjectID:   project.ID,
			ManagedRevision:    1,
			ManagedAgentName:   "reviewer",
			ManagedSchedulerID: scheduler.SchedulerID,
		},
		Script: "scheduler.interval(\"duplicate\", async function() {}, 1000);",
		Triggers: []LoaderTrigger{
			{ID: "duplicate", Kind: LoaderTriggerKindInterval, IntervalMs: 1000, Enabled: true, SpecJSON: `{"kind":"interval"}`},
			{ID: "duplicate", Kind: LoaderTriggerKindInterval, IntervalMs: 2000, Enabled: true, SpecJSON: `{"kind":"interval"}`},
		},
	}

	changes, unchanged, err := service.reconcileProjectManagedSchedulers(ctx, project, []ProjectSchedulerRecord{scheduler}, []Loader{loader})
	if err == nil || !strings.Contains(err.Error(), "duplicate loader trigger id") {
		t.Fatalf("reconcileProjectManagedSchedulers error = %v, want duplicate trigger error", err)
	}
	if unchanged || len(changes) != 0 {
		t.Fatalf("failed reconcile changes/unchanged = %#v/%v", changes, unchanged)
	}
	stagedScheduler, err := store.GetProjectScheduler(ctx, project.ID, scheduler.SchedulerID)
	if err != nil {
		t.Fatalf("GetProjectScheduler after failure returned error: %v", err)
	}
	if stagedScheduler.Enabled {
		t.Fatalf("failed reconcile left scheduler enabled: %#v", stagedScheduler)
	}
	stagedLoader, err := store.GetLoader(ctx, scheduler.ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader after failure returned error: %v", err)
	}
	if stagedLoader.Summary.Enabled || len(stagedLoader.Triggers) != 0 {
		t.Fatalf("failed reconcile left active loader: %#v triggers=%#v", stagedLoader.Summary, stagedLoader.Triggers)
	}
}

func TestProjectServiceApplyProjectValidationFailureDoesNotPersist(t *testing.T) {
	testProjectServiceApplyProjectValidationFailureDoesNotPersist(t)
}

func TestE2EProjectServiceApplyProjectValidationFailureDoesNotPersist(t *testing.T) {
	testProjectServiceApplyProjectValidationFailureDoesNotPersist(t)
}

func testProjectServiceApplyProjectValidationFailureDoesNotPersist(t *testing.T) {
	t.Helper()
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	ctx := context.Background()
	source := &agentcomposev2.ProjectSource{ComposePath: filepath.Join(t.TempDir(), "agent-compose.yml")}

	resp, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:             newProjectServiceTestSpec("demo", "gpt-test"),
		Source:           source,
		ExpectedSpecHash: "sha256:wrong",
	}))
	if err != nil {
		t.Fatalf("ApplyProject hash mismatch returned error: %v", err)
	}
	if resp.Msg.GetApplied() || len(resp.Msg.GetIssues()) != 1 {
		t.Fatalf("ApplyProject hash mismatch response = %#v", resp.Msg)
	}
	listed, err := store.ListProjects(ctx, ProjectListOptions{})
	if err != nil {
		t.Fatalf("ListProjects after failed apply returned error: %v", err)
	}
	if listed.TotalCount != 0 {
		t.Fatalf("failed ApplyProject persisted projects: %#v", listed.Projects)
	}
}

func newProjectServiceTestService(t *testing.T, store *ConfigStore) *Service {
	t.Helper()
	return &Service{
		config: &appconfig.Config{
			RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
			DefaultImage:  "guest:latest",
		},
		configDB: store,
		loaders: &LoaderManager{
			configDB:     store,
			engine:       &QJSLoaderEngine{},
			loaders:      map[string]Loader{},
			running:      map[string]int{},
			scheduleWake: make(chan struct{}, 1),
		},
		images: &fakeImageBackend{
			inspectImage: func(context.Context, ImageInspectRequest) (ImageInspectResult, error) {
				return ImageInspectResult{}, nil
			},
		},
	}
}

func newProjectServiceTestSpec(name string, reviewerModel string) *agentcomposev2.ProjectSpec {
	return &agentcomposev2.ProjectSpec{
		Name: name,
		Agents: []*agentcomposev2.AgentSpec{
			{
				Name:     "reviewer",
				Provider: "codex",
				Model:    reviewerModel,
				Image:    "guest:v1",
				Driver:   &agentcomposev2.DriverSpec{Name: "boxlite"},
				Scheduler: &agentcomposev2.SchedulerSpec{
					Enabled: true,
					Triggers: []*agentcomposev2.TriggerSpec{{
						Name:   "hourly",
						Kind:   "cron",
						Cron:   "0 * * * *",
						Prompt: "review",
					}},
				},
			},
			{
				Name:     "worker",
				Provider: "claude",
				Driver:   &agentcomposev2.DriverSpec{Name: "docker"},
			},
		},
	}
}

func newProjectServiceInlineSchedulerScriptSpec(name string, script string) *agentcomposev2.ProjectSpec {
	return &agentcomposev2.ProjectSpec{
		Name: name,
		Agents: []*agentcomposev2.AgentSpec{{
			Name:     "reviewer",
			Provider: "codex",
			Model:    "gpt-test",
			Image:    "guest:v1",
			Driver:   &agentcomposev2.DriverSpec{Name: "boxlite"},
			Scheduler: &agentcomposev2.SchedulerSpec{
				Enabled: true,
				Script:  script,
			},
		}},
	}
}

func assertProjectServiceAgent(t *testing.T, agents []*agentcomposev2.ProjectAgent, name string, schedulerEnabled bool, model string) {
	t.Helper()
	for _, agent := range agents {
		if agent.GetAgentName() != name {
			continue
		}
		if agent.GetManagedAgentId() == "" || agent.GetSchedulerEnabled() != schedulerEnabled || strings.TrimSpace(agent.GetModel()) != model {
			t.Fatalf("agent %s = %#v", name, agent)
		}
		return
	}
	t.Fatalf("agent %s not found in %#v", name, agents)
}

func managedAgentIDByName(t *testing.T, agents []*agentcomposev2.ProjectAgent, name string) string {
	t.Helper()
	for _, agent := range agents {
		if agent.GetAgentName() == name {
			if strings.TrimSpace(agent.GetManagedAgentId()) == "" {
				t.Fatalf("agent %s has empty managed id: %#v", name, agent)
			}
			return agent.GetManagedAgentId()
		}
	}
	t.Fatalf("agent %s not found in %#v", name, agents)
	return ""
}

func managedSchedulerIDByAgentName(t *testing.T, schedulers []*agentcomposev2.ProjectScheduler, agentName string) string {
	t.Helper()
	for _, scheduler := range schedulers {
		if scheduler.GetAgentName() == agentName {
			if strings.TrimSpace(scheduler.GetSchedulerId()) == "" {
				t.Fatalf("scheduler %s has empty scheduler id: %#v", agentName, scheduler)
			}
			return scheduler.GetSchedulerId()
		}
	}
	t.Fatalf("scheduler for agent %s not found in %#v", agentName, schedulers)
	return ""
}

func managedLoaderIDByAgentName(t *testing.T, schedulers []*agentcomposev2.ProjectScheduler, agentName string) string {
	t.Helper()
	for _, scheduler := range schedulers {
		if scheduler.GetAgentName() == agentName {
			if strings.TrimSpace(scheduler.GetManagedLoaderId()) == "" {
				t.Fatalf("scheduler %s has empty managed loader id: %#v", agentName, scheduler)
			}
			return scheduler.GetManagedLoaderId()
		}
	}
	t.Fatalf("scheduler for agent %s not found in %#v", agentName, schedulers)
	return ""
}

func assertManagedAgentDefinition(t *testing.T, store *ConfigStore, ctx context.Context, agentID, projectID, agentName string, revision int64, enabled bool) AgentDefinition {
	t.Helper()
	agent, err := store.GetAgentDefinitionIncludingDeleted(ctx, agentID)
	if err != nil {
		t.Fatalf("GetAgentDefinitionIncludingDeleted(%s) returned error: %v", agentID, err)
	}
	if agent.ManagedProjectID != projectID || agent.ManagedProjectRevision != revision || agent.ManagedAgentName != agentName || agent.Enabled != enabled {
		t.Fatalf("managed agent definition = %#v, want project=%s revision=%d agent=%s enabled=%v", agent, projectID, revision, agentName, enabled)
	}
	return agent
}

func assertManagedLoader(t *testing.T, store *ConfigStore, ctx context.Context, loaderID, projectID, agentName, schedulerID string, revision int64, enabled bool) Loader {
	t.Helper()
	loader, err := store.GetLoader(ctx, loaderID)
	if err != nil {
		t.Fatalf("GetLoader(%s) returned error: %v", loaderID, err)
	}
	if loader.Summary.ManagedProjectID != projectID ||
		loader.Summary.ManagedRevision != revision ||
		loader.Summary.ManagedAgentName != agentName ||
		loader.Summary.ManagedSchedulerID != schedulerID ||
		loader.Summary.Enabled != enabled {
		t.Fatalf("managed loader = %#v, want project=%s revision=%d agent=%s scheduler=%s enabled=%v", loader.Summary, projectID, revision, agentName, schedulerID, enabled)
	}
	return loader
}

func assertStringSliceEqual(t *testing.T, got, want []string, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %#v, want %#v", label, got, want)
		}
	}
}
