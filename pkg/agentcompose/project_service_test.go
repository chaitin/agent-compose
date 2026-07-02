package agentcompose

import (
	"context"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	imagespkg "agent-compose/pkg/images"
	loaderspkg "agent-compose/pkg/loaders"
	"agent-compose/pkg/storage"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func newProjectServiceTestService(t *testing.T, store *storage.ConfigStore) *Service {
	t.Helper()
	config := &appconfig.Config{
		RuntimeDriver: driverpkg.RuntimeDriverBoxlite,
		DefaultImage:  "guest:latest",
	}
	return &Service{
		config:   config,
		configDB: store,
		loaders:  newTestLoaderManager(t, loaderspkg.ManagerDeps{Config: config, ConfigDB: store, Engine: &loaderspkg.QJSLoaderEngine{}}),
		images: &fakeImageBackend{
			inspectImage: func(context.Context, imagespkg.ImageInspectRequest) (imagespkg.ImageInspectResult, error) {
				return imagespkg.ImageInspectResult{}, nil
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
