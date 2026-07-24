package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	dockerclient "github.com/docker/docker/client"
	"google.golang.org/protobuf/proto"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/storage/configstore"
	storagesqlite "agent-compose/pkg/storage/sqlite"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const legacyLoaderEnvE2EImageEnv = "AGENT_COMPOSE_E2E_LEGACY_LOADER_ENV_IMAGE"

func TestE2ELegacyLoaderEnvironmentSurvivesPublicProjectApply(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(legacyLoaderEnvE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to an existing local Docker image", legacyLoaderEnvE2EImageEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	repoRoot := e2eRepoRoot(t)
	testRoot, err := os.MkdirTemp(repoRoot, ".legacy-loader-env-e2e-")
	if err != nil {
		t.Fatalf("create legacy loader env E2E root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(testRoot) })
	dockerClient := newE2EDockerClient(t, ctx, image)
	databasePath := filepath.Join(testRoot, "data.db")
	seedLegacyLoaderEnvironment(t, ctx, databasePath, image)

	binary := e2eDaemonBinary(t, ctx, repoRoot, testRoot)
	listenAddress := unusedLoopbackAddress(t)
	baseURL := "http://" + listenAddress
	daemon := startE2EDaemon(t, binary, repoRoot, testRoot, listenAddress, image)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("agent-compose daemon log:\n%s", daemon.logs.String())
		}
	})

	database, store := openLegacyLoaderE2EStore(t, databasePath)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close E2E inspection database: %v", err)
		}
	})
	httpClient := newE2EHTTPClient()
	t.Cleanup(httpClient.CloseIdleConnections)
	client := agentcomposev2connect.NewProjectServiceClient(httpClient, baseURL)
	settingsClient := agentcomposev2connect.NewSettingsServiceClient(httpClient, baseURL)
	sandboxClient := agentcomposev2connect.NewSandboxServiceClient(httpClient, baseURL)
	updateE2EGlobalEnv(t, ctx, settingsClient)

	normalProject := applyOrdinaryEnvProject(t, ctx, client, image)
	normalBefore := getE2EProject(t, ctx, client, normalProject.GetSummary().GetProjectId())
	normalLoaderBefore := managedLoaderForE2EProject(t, ctx, store, normalProject.GetSummary().GetProjectId())

	legacyProjectID, err := domain.StableProjectID(projects.LegacyDefaultProjectName, "")
	if err != nil {
		t.Fatalf("resolve legacy default project ID: %v", err)
	}
	legacyBefore := getE2EProject(t, ctx, client, legacyProjectID)
	if legacyBefore.GetSummary().GetSourcePath() != "" || len(legacyBefore.GetAgents()) != 1 || len(legacyBefore.GetSchedulers()) != 1 {
		t.Fatalf("legacy project shape = %#v", legacyBefore)
	}
	initialLoader := managedLoaderForE2EProject(t, ctx, store, legacyProjectID)
	if initialLoader.Summary.AgentID != legacyBefore.GetAgents()[0].GetManagedAgentId() {
		t.Fatalf(
			"initial adopted loader agent ID = %q, want managed agent %q",
			initialLoader.Summary.AgentID,
			legacyBefore.GetAgents()[0].GetManagedAgentId(),
		)
	}
	assertE2EEnvItems(t, "initial adopted loader env", initialLoader.EnvItems, legacySeedLoaderEnv())

	manualLoaderEnv := []domain.SandboxEnvVar{
		{Name: "AGENT_LOADER", Value: "manual-agent-loader"},
		{Name: "GLOBAL_AGENT_LOADER", Value: "manual-global-agent-loader"},
		{Name: "LOADER_ADDED", Value: "manual-added"},
		{Name: "LOADER_STAYS", Value: "manual-stays", Secret: true},
		{Name: "REMOVE_ME", Value: "manual-remove"},
		{Name: "REQUEST_WINS", Value: "manual-request-wins"},
		{Name: "SECRET_FLAG", Value: "manual-secret"},
		{Name: "SHARED", Value: "manual-shared", Secret: true},
	}
	// Simulate persisted state from an older daemon, which adopted the Loader
	// identity but left it bound to the original v1 Agent.
	initialLoader.Summary.AgentID = "legacy-env-e2e-agent"
	initialLoader.EnvItems = manualLoaderEnv
	if _, err := store.UpdateLoader(ctx, initialLoader); err != nil {
		t.Fatalf("simulate out-of-band legacy loader env edit: %v", err)
	}

	scriptOnlySpec := proto.Clone(legacyBefore.GetSpec()).(*agentcomposev2.ProjectSpec)
	scriptOnlySpec.Agents[0].Scheduler.Script += "\n// public script-only update"
	dryRun, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   scriptOnlySpec,
		DryRun: true,
	}))
	if err != nil {
		t.Fatalf("dry-run public ApplyProject returned error: %v", err)
	}
	if dryRun.Msg.GetApplied() || len(dryRun.Msg.GetIssues()) != 0 {
		t.Fatalf("dry-run public ApplyProject = %#v", dryRun.Msg)
	}
	legacyAfterDryRun := getE2EProject(t, ctx, client, legacyProjectID)
	assertE2EProjectRevisionUnchanged(t, "legacy project after dry-run", legacyAfterDryRun, legacyBefore)
	assertE2EEnvItems(
		t,
		"legacy loader env after dry-run",
		managedLoaderForE2EProject(t, ctx, store, legacyProjectID).EnvItems,
		manualLoaderEnv,
	)

	scriptOnly, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: scriptOnlySpec}))
	if err != nil {
		t.Fatalf("public script-only ApplyProject returned error: %v", err)
	}
	if !scriptOnly.Msg.GetApplied() || len(scriptOnly.Msg.GetIssues()) != 0 {
		t.Fatalf("public script-only ApplyProject = %#v", scriptOnly.Msg)
	}
	legacyAfterScript := getE2EProject(t, ctx, client, legacyProjectID)
	if got, want := legacyAfterScript.GetSummary().GetCurrentRevision(), legacyBefore.GetSummary().GetCurrentRevision()+1; got != want {
		t.Fatalf("legacy revision after public script-only apply = %d, want %d", got, want)
	}
	assertE2EEnvItems(
		t,
		"legacy loader env after public script-only apply",
		managedLoaderForE2EProject(t, ctx, store, legacyProjectID).EnvItems,
		manualLoaderEnv,
	)
	legacyLoaderAfterScript := managedLoaderForE2EProject(t, ctx, store, legacyProjectID)
	if legacyLoaderAfterScript.Summary.AgentID != legacyAfterScript.GetAgents()[0].GetManagedAgentId() {
		t.Fatalf(
			"script-only apply loader agent ID = %q, want managed agent %q",
			legacyLoaderAfterScript.Summary.AgentID,
			legacyAfterScript.GetAgents()[0].GetManagedAgentId(),
		)
	}
	assertE2EProjectRevisionUnchanged(
		t,
		"ordinary project after legacy script-only apply",
		getE2EProject(t, ctx, client, normalProject.GetSummary().GetProjectId()),
		normalBefore,
	)
	assertE2EEnvItems(
		t,
		"ordinary loader after legacy script-only apply",
		managedLoaderForE2EProject(t, ctx, store, normalProject.GetSummary().GetProjectId()).EnvItems,
		normalLoaderBefore.EnvItems,
	)
	runLegacyEnvPriorityE2E(t, ctx, client, sandboxClient, dockerClient, legacyAfterScript, map[string]string{
		"AGENT_LOADER":        "manual-agent-loader",
		"DELETED_MANUALLY":    "",
		"GLOBAL_AGENT_LOADER": "manual-global-agent-loader",
		"GLOBAL_ONLY":         "global-only",
		"LOADER_STAYS":        "manual-stays",
		"REMOVE_ME":           "manual-remove",
		"REQUEST_WINS":        "request-value",
		"SHARED":              "manual-shared",
	})

	explicitEnvSpec := proto.Clone(legacyAfterScript.GetSpec()).(*agentcomposev2.ProjectSpec)
	setE2EEnv(explicitEnvSpec.Agents[0], "AGENT_LOADER", "agent-new", false)
	setE2EEnv(explicitEnvSpec.Agents[0], "SHARED", "agent-new", false)
	removeE2EEnv(explicitEnvSpec.Agents[0], "REMOVE_ME")
	setE2EEnv(explicitEnvSpec.Agents[0], "LOADER_ADDED", "agent-claims-key", true)
	setE2EEnv(explicitEnvSpec.Agents[0], "SECRET_FLAG", "same", true)
	explicitEnv, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: explicitEnvSpec}))
	if err != nil {
		t.Fatalf("public explicit env ApplyProject returned error: %v", err)
	}
	if !explicitEnv.Msg.GetApplied() || len(explicitEnv.Msg.GetIssues()) != 0 {
		t.Fatalf("public explicit env ApplyProject = %#v", explicitEnv.Msg)
	}
	finalLegacy := getE2EProject(t, ctx, client, legacyProjectID)
	finalLegacyLoader := managedLoaderForE2EProject(t, ctx, store, legacyProjectID)
	if finalLegacyLoader.Summary.AgentID != finalLegacy.GetAgents()[0].GetManagedAgentId() {
		t.Fatalf(
			"final adopted loader agent ID = %q, want managed agent %q",
			finalLegacyLoader.Summary.AgentID,
			finalLegacy.GetAgents()[0].GetManagedAgentId(),
		)
	}
	assertE2EEnvItems(t, "legacy loader overlay after explicit env apply", finalLegacyLoader.EnvItems, []domain.SandboxEnvVar{
		{Name: "GLOBAL_AGENT_LOADER", Value: "manual-global-agent-loader"},
		{Name: "LOADER_STAYS", Value: "manual-stays", Secret: true},
		{Name: "REQUEST_WINS", Value: "manual-request-wins"},
	})
	finalLegacyAgent, err := store.GetAgentDefinition(ctx, finalLegacy.GetAgents()[0].GetManagedAgentId())
	if err != nil {
		t.Fatalf("load final managed legacy agent: %v", err)
	}
	assertE2EEnvItems(t, "effective legacy env after explicit env apply", domain.MergeEnvItems(finalLegacyAgent.EnvItems, finalLegacyLoader.EnvItems), []domain.SandboxEnvVar{
		{Name: "AGENT_ONLY", Value: "agent-only"},
		{Name: "AGENT_LOADER", Value: "agent-new"},
		{Name: "GLOBAL_AGENT_LOADER", Value: "manual-global-agent-loader"},
		{Name: "LOADER_ADDED", Value: "agent-claims-key", Secret: true},
		{Name: "LOADER_STAYS", Value: "manual-stays", Secret: true},
		{Name: "REQUEST_WINS", Value: "manual-request-wins"},
		{Name: "SECRET_FLAG", Value: "same", Secret: true},
		{Name: "SHARED", Value: "agent-new"},
	})
	runLegacyEnvPriorityE2E(t, ctx, client, sandboxClient, dockerClient, finalLegacy, map[string]string{
		"AGENT_LOADER":        "agent-new",
		"DELETED_MANUALLY":    "",
		"GLOBAL_AGENT_LOADER": "manual-global-agent-loader",
		"GLOBAL_ONLY":         "global-only",
		"LOADER_STAYS":        "manual-stays",
		"REMOVE_ME":           "",
		"REQUEST_WINS":        "request-value",
		"SHARED":              "agent-new",
	})
	assertE2EProjectRevisionUnchanged(
		t,
		"ordinary project after legacy explicit env apply",
		getE2EProject(t, ctx, client, normalProject.GetSummary().GetProjectId()),
		normalBefore,
	)

	manuallyEditedNormal := managedLoaderForE2EProject(t, ctx, store, normalProject.GetSummary().GetProjectId())
	manuallyEditedNormal.EnvItems = []domain.SandboxEnvVar{{Name: "NORMAL", Value: "out-of-band"}}
	if _, err := store.UpdateLoader(ctx, manuallyEditedNormal); err != nil {
		t.Fatalf("simulate out-of-band ordinary loader env edit: %v", err)
	}
	normalScriptOnlySpec := proto.Clone(normalBefore.GetSpec()).(*agentcomposev2.ProjectSpec)
	normalScriptOnlySpec.Agents[0].Scheduler.Script += "\n// ordinary script-only update"
	normalScriptOnly, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{Spec: normalScriptOnlySpec}))
	if err != nil {
		t.Fatalf("ordinary public script-only ApplyProject returned error: %v", err)
	}
	if !normalScriptOnly.Msg.GetApplied() || len(normalScriptOnly.Msg.GetIssues()) != 0 {
		t.Fatalf("ordinary public script-only ApplyProject = %#v", normalScriptOnly.Msg)
	}
	normalLoaderAfter := managedLoaderForE2EProject(t, ctx, store, normalProject.GetSummary().GetProjectId())
	if normalLoaderAfter.Summary.ID != normalLoaderBefore.Summary.ID {
		t.Fatalf("ordinary managed loader ID changed from %q to %q", normalLoaderBefore.Summary.ID, normalLoaderAfter.Summary.ID)
	}
	assertE2EEnvItems(t, "ordinary loader env remains ProjectSpec-authoritative", normalLoaderAfter.EnvItems, normalLoaderBefore.EnvItems)
	legacyAfterOrdinaryApply := getE2EProject(t, ctx, client, legacyProjectID)
	assertE2EProjectRevisionUnchanged(t, "legacy project after ordinary project apply", legacyAfterOrdinaryApply, finalLegacy)
	assertE2EEnvItems(
		t,
		"legacy loader overlay after ordinary project apply",
		managedLoaderForE2EProject(t, ctx, store, legacyProjectID).EnvItems,
		finalLegacyLoader.EnvItems,
	)

	httpClient.CloseIdleConnections()
	daemon.stop(t)
	assertE2EDaemonReleased(t, daemon, filepath.Join(testRoot, "agent-compose.sock"), listenAddress)
}

func seedLegacyLoaderEnvironment(t *testing.T, ctx context.Context, databasePath, image string) {
	t.Helper()
	database, err := storagesqlite.Open(databasePath, 16*time.Second)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	store := configstore.FromDB(database.DB())
	agent, err := store.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID:         "legacy-env-e2e-agent",
		Name:       "legacy-env-e2e-agent",
		Enabled:    true,
		Provider:   "codex",
		Driver:     driverpkg.RuntimeDriverDocker,
		GuestImage: image,
		EnvItems: []domain.SandboxEnvVar{
			{Name: "AGENT_ONLY", Value: "agent-only"},
			{Name: "AGENT_LOADER", Value: "agent"},
			{Name: "GLOBAL_AGENT_LOADER", Value: "agent"},
			{Name: "REMOVE_ME", Value: "agent-remove"},
			{Name: "REQUEST_WINS", Value: "agent"},
			{Name: "SECRET_FLAG", Value: "same"},
			{Name: "SHARED", Value: "agent-old"},
		},
	})
	if err != nil {
		_ = database.Close()
		t.Fatalf("seed legacy agent: %v", err)
	}
	_, err = store.CreateLoader(ctx, domain.Loader{
		Summary: domain.LoaderSummary{
			ID:                "legacy-env-e2e-loader",
			Name:              "Legacy env E2E scheduler",
			Enabled:           true,
			Runtime:           domain.LoaderRuntimeScheduler,
			AgentID:           agent.ID,
			Driver:            driverpkg.RuntimeDriverDocker,
			GuestImage:        image,
			DefaultAgent:      "codex",
			SandboxPolicy:     domain.LoaderSandboxPolicyNew,
			ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip,
		},
		Script:   legacyEnvRuntimeScript,
		EnvItems: legacySeedLoaderEnv(),
	})
	if err != nil {
		_ = database.Close()
		t.Fatalf("seed legacy loader: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
}

func legacySeedLoaderEnv() []domain.SandboxEnvVar {
	return []domain.SandboxEnvVar{
		{Name: "AGENT_LOADER", Value: "legacy-agent-loader"},
		{Name: "DELETED_MANUALLY", Value: "legacy-delete"},
		{Name: "GLOBAL_AGENT_LOADER", Value: "legacy-global-agent-loader"},
		{Name: "LOADER_STAYS", Value: "legacy-stays"},
		{Name: "REMOVE_ME", Value: "legacy-remove"},
		{Name: "REQUEST_WINS", Value: "legacy-request-wins"},
		{Name: "SECRET_FLAG", Value: "legacy-secret", Secret: true},
		{Name: "SHARED", Value: "legacy-shared", Secret: true},
	}
}

const legacyEnvRuntimeScript = `
scheduler.on("legacy.env.e2e", "legacy-env-e2e", function legacyEnvE2E() {
  const result = scheduler.shell(
    "printf '%s\\n' \"GLOBAL_ONLY=$GLOBAL_ONLY\" \"GLOBAL_AGENT_LOADER=$GLOBAL_AGENT_LOADER\" \"AGENT_LOADER=$AGENT_LOADER\" \"REQUEST_WINS=$REQUEST_WINS\" \"SHARED=$SHARED\" \"LOADER_STAYS=$LOADER_STAYS\" \"REMOVE_ME=$REMOVE_ME\" \"DELETED_MANUALLY=$DELETED_MANUALLY\"",
    {
      sandboxPolicy: "new",
      title: "legacy env precedence e2e",
      env: { REQUEST_WINS: "request-value" }
    }
  );
  if (!result.success) {
    throw new Error("environment probe failed");
  }
  return { output: result.output || result.stdout || "" };
});
`

func openLegacyLoaderE2EStore(t *testing.T, databasePath string) (*storagesqlite.Database, *configstore.ConfigStore) {
	t.Helper()
	database, err := storagesqlite.Open(databasePath, 16*time.Second)
	if err != nil {
		t.Fatalf("open E2E inspection database: %v", err)
	}
	return database, configstore.FromDB(database.DB())
}

func applyOrdinaryEnvProject(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ProjectServiceClient,
	image string,
) *agentcomposev2.Project {
	t.Helper()
	response, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "ordinary-env-e2e",
			Agents: []*agentcomposev2.AgentSpec{{
				Name:     "worker",
				Provider: "codex",
				Image:    image,
				Driver:   &agentcomposev2.DriverSpec{Name: "docker", Docker: &agentcomposev2.DockerDriverSpec{}},
				Env: []*agentcomposev2.EnvVarSpec{
					{Name: "NORMAL", Value: "project-value"},
					{Name: "NORMAL_SECRET", Value: "project-secret", Secret: true},
				},
				Scheduler: &agentcomposev2.SchedulerSpec{
					Enabled: true,
					Script:  `scheduler.on("ordinary.env.e2e", "ordinary-env-e2e", function ordinaryEnvE2E() {});`,
				},
			}},
		},
	}))
	if err != nil {
		t.Fatalf("apply ordinary env project: %v", err)
	}
	if !response.Msg.GetApplied() || len(response.Msg.GetIssues()) != 0 {
		t.Fatalf("ordinary env project apply = %#v", response.Msg)
	}
	return response.Msg.GetProject()
}

func updateE2EGlobalEnv(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.SettingsServiceClient,
) {
	t.Helper()
	response, err := client.UpdateGlobalEnv(ctx, connect.NewRequest(&agentcomposev2.UpdateGlobalEnvRequest{
		Env: []*agentcomposev2.EnvVarUpdateSpec{
			{Name: "GLOBAL_ONLY", Value: proto.String("global-only")},
			{Name: "GLOBAL_AGENT_LOADER", Value: proto.String("global")},
			{Name: "REQUEST_WINS", Value: proto.String("global")},
		},
	}))
	if err != nil {
		t.Fatalf("UpdateGlobalEnv for runtime precedence: %v", err)
	}
	if len(response.Msg.GetEnv()) != 3 {
		t.Fatalf("UpdateGlobalEnv returned %d items, want 3", len(response.Msg.GetEnv()))
	}
}

func runLegacyEnvPriorityE2E(
	t *testing.T,
	ctx context.Context,
	projectClient agentcomposev2connect.ProjectServiceClient,
	sandboxClient agentcomposev2connect.SandboxServiceClient,
	dockerClient *dockerclient.Client,
	project *agentcomposev2.Project,
	want map[string]string,
) {
	t.Helper()
	response, err := projectClient.RunScheduler(ctx, connect.NewRequest(&agentcomposev2.RunSchedulerRequest{
		Project:     &agentcomposev2.ProjectRef{ProjectId: project.GetSummary().GetProjectId()},
		AgentName:   project.GetAgents()[0].GetAgentName(),
		TriggerId:   "legacy-env-e2e",
		PayloadJson: `{}`,
	}))
	if err != nil {
		t.Fatalf("RunScheduler environment precedence probe: %v", err)
	}
	run := response.Msg.GetRun()
	if run.GetStatus() != agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED {
		t.Fatalf("environment precedence run status = %s, error = %q", run.GetStatus(), run.GetError())
	}
	sandboxID := linkedE2ESandboxID(t, ctx, projectClient, project, run.GetRunId())
	removeLegacyEnvE2ESandboxPublic(t, ctx, sandboxClient, sandboxID)
	removeE2EDockerSandboxFallback(t, ctx, dockerClient, sandboxID)
	var result struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal([]byte(run.GetResultJson()), &result); err != nil {
		t.Fatalf("decode environment precedence result %q: %v", run.GetResultJson(), err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(result.Output), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			got[name] = value
		}
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("runtime env %s = %q, want %q; output = %q", name, got[name], value, result.Output)
		}
	}
}

func removeLegacyEnvE2ESandboxPublic(
	t *testing.T,
	ctx context.Context,
	sandboxClient agentcomposev2connect.SandboxServiceClient,
	sandboxID string,
) {
	t.Helper()
	response, err := sandboxClient.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{
		SandboxId: sandboxID,
		Force:     true,
	}))
	if err != nil {
		t.Fatalf("RemoveSandbox %s returned error: %v", sandboxID, err)
	}
	if response.Msg.GetSandboxId() != sandboxID || !response.Msg.GetRemoved() {
		t.Fatalf("RemoveSandbox %s response = %#v, want removed sandbox", sandboxID, response.Msg)
	}
}

func linkedE2ESandboxID(
	t *testing.T,
	ctx context.Context,
	projectClient agentcomposev2connect.ProjectServiceClient,
	project *agentcomposev2.Project,
	runID string,
) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := projectClient.ListProjectSchedulerEvents(ctx, connect.NewRequest(&agentcomposev2.ListProjectSchedulerEventsRequest{
			Project:   &agentcomposev2.ProjectRef{ProjectId: project.GetSummary().GetProjectId()},
			AgentName: project.GetAgents()[0].GetAgentName(),
			RunId:     runID,
			Limit:     100,
		}))
		if err != nil {
			t.Fatalf("ListProjectSchedulerEvents for run %s: %v", runID, err)
		}
		sandboxIDs := make(map[string]struct{})
		for _, event := range response.Msg.GetEvents() {
			if sandboxID := strings.TrimSpace(event.GetLinkedSandboxId()); sandboxID != "" {
				sandboxIDs[sandboxID] = struct{}{}
			}
		}
		if len(sandboxIDs) == 1 {
			for sandboxID := range sandboxIDs {
				return sandboxID
			}
		}
		if len(sandboxIDs) > 1 {
			t.Fatalf("scheduler run %s linked sandbox IDs = %v, want one", runID, sandboxIDs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("scheduler run %s did not expose a linked sandbox event", runID)
	return ""
}

func getE2EProject(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ProjectServiceClient,
	projectID string,
) *agentcomposev2.Project {
	t.Helper()
	response, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
		IncludeSpec: true,
	}))
	if err != nil {
		t.Fatalf("GetProject %s: %v", projectID, err)
	}
	if response.Msg.GetProject().GetSpec() == nil {
		t.Fatalf("GetProject %s returned no spec", projectID)
	}
	return response.Msg.GetProject()
}

func managedLoaderForE2EProject(
	t *testing.T,
	ctx context.Context,
	store *configstore.ConfigStore,
	projectID string,
) domain.Loader {
	t.Helper()
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("list project %s schedulers: %v", projectID, err)
	}
	if len(schedulers) != 1 {
		t.Fatalf("project %s scheduler count = %d, want 1", projectID, len(schedulers))
	}
	loader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("load project %s managed loader %s: %v", projectID, schedulers[0].ManagedLoaderID, err)
	}
	return loader
}

func assertE2EProjectRevisionUnchanged(t *testing.T, label string, got, want *agentcomposev2.Project) {
	t.Helper()
	if got.GetSummary().GetCurrentRevision() != want.GetSummary().GetCurrentRevision() ||
		got.GetSummary().GetSpecHash() != want.GetSummary().GetSpecHash() {
		t.Fatalf(
			"%s revision/hash = %d/%q, want %d/%q",
			label,
			got.GetSummary().GetCurrentRevision(),
			got.GetSummary().GetSpecHash(),
			want.GetSummary().GetCurrentRevision(),
			want.GetSummary().GetSpecHash(),
		)
	}
}

func assertE2EEnvItems(t *testing.T, label string, got, want []domain.SandboxEnvVar) {
	t.Helper()
	if !projects.SameSandboxEnvItems(got, want) {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
}

func setE2EEnv(agent *agentcomposev2.AgentSpec, name, value string, secret bool) {
	for _, item := range agent.Env {
		if item.GetName() == name {
			item.Value = value
			item.Secret = secret
			return
		}
	}
	agent.Env = append(agent.Env, &agentcomposev2.EnvVarSpec{Name: name, Value: value, Secret: secret})
}

func removeE2EEnv(agent *agentcomposev2.AgentSpec, name string) {
	filtered := agent.Env[:0]
	for _, item := range agent.Env {
		if item.GetName() != name {
			filtered = append(filtered, item)
		}
	}
	agent.Env = filtered
}
