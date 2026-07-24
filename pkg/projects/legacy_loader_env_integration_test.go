package projects_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

func TestIntegrationLegacyLoaderEnvironmentSurvivesProjectApply(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:           root,
		DbAddr:             filepath.Join(root, "data.db"),
		RuntimeDriver:      driverpkg.RuntimeDriverDocker,
		DockerDefaultImage: "guest:latest",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("create config store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.DB().Close(); err != nil {
			t.Errorf("close config store: %v", err)
		}
	})

	agentEnv := []domain.SandboxEnvVar{
		{Name: "AGENT_ONLY", Value: "agent-only"},
		{Name: "REMOVE_ME", Value: "agent-remove"},
		{Name: "SECRET_FLAG", Value: "same"},
		{Name: "SHARED", Value: "agent-old"},
	}
	agent, err := store.CreateAgentDefinition(ctx, domain.AgentDefinition{
		ID:         "legacy-env-agent",
		Name:       "env-worker",
		Enabled:    true,
		Provider:   "codex",
		Driver:     driverpkg.RuntimeDriverDocker,
		GuestImage: "guest:latest",
		EnvItems:   agentEnv,
	})
	if err != nil {
		t.Fatalf("create legacy agent: %v", err)
	}

	legacyLoaderEnv := []domain.SandboxEnvVar{
		{Name: "DELETED_MANUALLY", Value: "legacy-delete"},
		{Name: "LOADER_STAYS", Value: "legacy-stays"},
		{Name: "REMOVE_ME", Value: "legacy-remove"},
		{Name: "SECRET_FLAG", Value: "legacy-secret", Secret: true},
		{Name: "SHARED", Value: "legacy-shared", Secret: true},
	}
	const script = `scheduler.on("env.test", "env-test", function envTest() {});`
	loader, err := store.CreateLoader(ctx, domain.Loader{
		Summary: domain.LoaderSummary{
			ID:                "legacy-env-loader",
			Name:              "Legacy env scheduler",
			Enabled:           true,
			Runtime:           domain.LoaderRuntimeScheduler,
			AgentID:           agent.ID,
			Driver:            driverpkg.RuntimeDriverDocker,
			GuestImage:        "guest:latest",
			DefaultAgent:      "codex",
			SandboxPolicy:     domain.LoaderSandboxPolicyNew,
			ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip,
		},
		Script:   script,
		EnvItems: legacyLoaderEnv,
	})
	if err != nil {
		t.Fatalf("create legacy loader: %v", err)
	}

	engine := &loaders.QJSLoaderEngine{}
	controller := projects.NewController(projects.ControllerDependencies{
		Config:  config,
		Store:   store,
		Images:  legacyProjectImageBackend{},
		Loaders: legacyProjectLoaderValidator{engine: engine},
		Volumes: legacyProjectVolumeManager{},
	})
	migrated, err := controller.SyncLegacyDefaultProject(ctx)
	if err != nil || !migrated.Applied || len(migrated.Issues) != 0 {
		t.Fatalf("sync legacy default project = %#v, err = %v", migrated, err)
	}
	if len(migrated.Agents) != 1 || len(migrated.Schedulers) != 1 || migrated.RevisionSpec == nil {
		t.Fatalf("migrated project artifacts = %#v", migrated)
	}

	adopted, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("load adopted loader: %v", err)
	}
	assertLegacyEnvItems(t, "initial adopted loader env", adopted.EnvItems, legacyLoaderEnv)

	manualLoaderEnv := []domain.SandboxEnvVar{
		{Name: "LOADER_ADDED", Value: "manual-added"},
		{Name: "LOADER_STAYS", Value: "manual-stays", Secret: true},
		{Name: "REMOVE_ME", Value: "manual-remove"},
		{Name: "SECRET_FLAG", Value: "manual-secret"},
		{Name: "SHARED", Value: "manual-shared", Secret: true},
	}
	adopted.EnvItems = manualLoaderEnv
	if _, err := store.UpdateLoader(ctx, adopted); err != nil {
		t.Fatalf("simulate loader env edit: %v", err)
	}

	scriptOnlySpec := cloneLegacyEnvProjectSpec(migrated.RevisionSpec)
	scriptOnlySpec.Agents[0].Scheduler.Script += "\n// script-only update"
	scriptOnlyProject := normalizedLegacyEnvProject(t, &scriptOnlySpec)
	dryRun, err := controller.ApplyProject(ctx, projects.ApplyRequest{Normalized: scriptOnlyProject, DryRun: true})
	if err != nil || dryRun.Applied || len(dryRun.Issues) != 0 {
		t.Fatalf("dry-run script-only apply = %#v, err = %v", dryRun, err)
	}
	afterDryRun, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("load loader after dry-run: %v", err)
	}
	assertLegacyEnvItems(t, "loader env after dry-run", afterDryRun.EnvItems, manualLoaderEnv)

	scriptOnly, err := controller.ApplyProject(ctx, projects.ApplyRequest{Normalized: scriptOnlyProject})
	if err != nil || !scriptOnly.Applied || len(scriptOnly.Issues) != 0 {
		t.Fatalf("script-only apply = %#v, err = %v", scriptOnly, err)
	}
	afterScriptOnly, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("load loader after script-only apply: %v", err)
	}
	assertLegacyEnvItems(t, "loader env after script-only apply", afterScriptOnly.EnvItems, manualLoaderEnv)

	managedAgent, err := store.GetAgentDefinition(ctx, migrated.Agents[0].ManagedAgentID)
	if err != nil {
		t.Fatalf("load managed agent after script-only apply: %v", err)
	}
	effective := legacyEnvItemsByName(domain.MergeEnvItems(managedAgent.EnvItems, afterScriptOnly.EnvItems))
	if effective["SHARED"].Value != "manual-shared" || effective["SHARED"].Secret != true {
		t.Fatalf("loader env did not override agent env after script-only apply: %#v", effective["SHARED"])
	}
	if _, exists := effective["DELETED_MANUALLY"]; exists {
		t.Fatalf("manually deleted loader env was restored: %#v", effective["DELETED_MANUALLY"])
	}

	explicitEnvSpec := cloneLegacyEnvProjectSpec(&scriptOnlySpec)
	explicitEnvSpec.Agents[0].Env["SHARED"] = compose.EnvVarSpec{Value: "agent-new"}
	delete(explicitEnvSpec.Agents[0].Env, "REMOVE_ME")
	explicitEnvSpec.Agents[0].Env["LOADER_ADDED"] = compose.EnvVarSpec{Value: "agent-claims-key", Secret: true}
	explicitEnvSpec.Agents[0].Env["SECRET_FLAG"] = compose.EnvVarSpec{Value: "same", Secret: true}

	explicitEnv, err := controller.ApplyProject(ctx, projects.ApplyRequest{Normalized: normalizedLegacyEnvProject(t, &explicitEnvSpec)})
	if err != nil || !explicitEnv.Applied || len(explicitEnv.Issues) != 0 {
		t.Fatalf("explicit agent env apply = %#v, err = %v", explicitEnv, err)
	}
	finalLoader, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("load loader after explicit env apply: %v", err)
	}
	assertLegacyEnvItems(t, "loader overlay after explicit env apply", finalLoader.EnvItems, []domain.SandboxEnvVar{
		{Name: "LOADER_STAYS", Value: "manual-stays", Secret: true},
	})

	finalAgent, err := store.GetAgentDefinition(ctx, migrated.Agents[0].ManagedAgentID)
	if err != nil {
		t.Fatalf("load managed agent after explicit env apply: %v", err)
	}
	effective = legacyEnvItemsByName(domain.MergeEnvItems(finalAgent.EnvItems, finalLoader.EnvItems))
	for name, want := range map[string]domain.SandboxEnvVar{
		"AGENT_ONLY":   {Name: "AGENT_ONLY", Value: "agent-only"},
		"LOADER_ADDED": {Name: "LOADER_ADDED", Value: "agent-claims-key", Secret: true},
		"LOADER_STAYS": {Name: "LOADER_STAYS", Value: "manual-stays", Secret: true},
		"SECRET_FLAG":  {Name: "SECRET_FLAG", Value: "same", Secret: true},
		"SHARED":       {Name: "SHARED", Value: "agent-new"},
	} {
		if got := effective[name]; got != want {
			t.Fatalf("effective env %s = %#v, want %#v", name, got, want)
		}
	}
	if _, exists := effective["REMOVE_ME"]; exists {
		t.Fatalf("explicitly removed agent env still effective: %#v", effective["REMOVE_ME"])
	}
}

func cloneLegacyEnvProjectSpec(spec *compose.NormalizedProjectSpec) compose.NormalizedProjectSpec {
	cloned := *spec
	cloned.Agents = append([]compose.NormalizedAgentSpec(nil), spec.Agents...)
	for index := range cloned.Agents {
		if spec.Agents[index].Scheduler != nil {
			scheduler := *spec.Agents[index].Scheduler
			cloned.Agents[index].Scheduler = &scheduler
		}
		if spec.Agents[index].Env != nil {
			cloned.Agents[index].Env = make(map[string]compose.EnvVarSpec, len(spec.Agents[index].Env))
			for name, value := range spec.Agents[index].Env {
				cloned.Agents[index].Env[name] = value
			}
		}
	}
	return cloned
}

func normalizedLegacyEnvProject(t *testing.T, spec *compose.NormalizedProjectSpec) projects.NormalizedProject {
	t.Helper()
	hash, err := spec.Hash()
	if err != nil {
		t.Fatalf("hash legacy env project: %v", err)
	}
	return projects.NormalizedProject{Spec: spec, SpecHash: hash}
}

func assertLegacyEnvItems(t *testing.T, label string, got, want []domain.SandboxEnvVar) {
	t.Helper()
	if !projects.SameSandboxEnvItems(got, want) {
		t.Fatalf("%s = %#v, want %#v", label, got, want)
	}
}

func legacyEnvItemsByName(items []domain.SandboxEnvVar) map[string]domain.SandboxEnvVar {
	byName := make(map[string]domain.SandboxEnvVar, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		byName[item.Name] = item
	}
	return byName
}
