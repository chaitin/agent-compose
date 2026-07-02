package agentcompose

import (
	"context"
	"testing"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	loaderspkg "agent-compose/pkg/loaders"
	projectspkg "agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func newProjectServiceTestService(t *testing.T, store *ConfigStore) *Service {
	t.Helper()
	config := &appconfig.Config{
		RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
		DefaultImage:  "guest:latest",
	}
	return &Service{
		config:   config,
		configDB: store,
		loaders:  newTestLoaderManager(t, loaderspkg.ManagerDeps{Config: config, ConfigDB: store, Engine: &QJSLoaderEngine{}}),
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

func projectSpecResponse(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	return projectspkg.ProjectSpecResponse(spec)
}
