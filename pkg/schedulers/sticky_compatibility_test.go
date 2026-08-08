package schedulers

import (
	"slices"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestNormalizeStickySandboxVolumeMountsUsesCanonicalOrder(t *testing.T) {
	mounts := []domain.SandboxVolumeMount{
		{ID: "z", Type: domain.VolumeMountTypeVolume, Source: "data", Target: "/workspace/data", HostPath: "/volumes/data", VolumeID: "volume-1"},
		{ID: "b", Type: domain.VolumeMountTypeBind, Source: "./data", Target: "/workspace/data", HostPath: "/project/data", ProjectPath: "/project"},
		{ID: "a", Type: domain.VolumeMountTypeBind, Source: "./cache", Target: "/workspace/cache", HostPath: "/project/cache", ReadOnly: true, ProjectPath: "/project"},
	}
	reordered := []domain.SandboxVolumeMount{mounts[2], mounts[0], mounts[1]}

	first := NormalizeStickySandboxVolumeMounts(mounts)
	second := NormalizeStickySandboxVolumeMounts(reordered)
	if !slices.Equal(first, second) {
		t.Fatalf("canonical mounts differ by input order:\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if first[0].Target != "/workspace/cache" || first[1].Type != domain.VolumeMountTypeBind || first[2].Type != domain.VolumeMountTypeVolume {
		t.Fatalf("canonical mount order = %#v", first)
	}
}

func TestSchedulerSandboxConfigHashTracksSandboxSemantics(t *testing.T) {
	base := domain.Scheduler{
		Summary: domain.SchedulerSummary{
			ID:            "scheduler-1",
			Name:          "Scheduler",
			Runtime:       domain.SchedulerRuntimeScheduler,
			DefaultAgent:  "codex",
			SandboxPolicy: domain.SchedulerSandboxPolicySticky,
			CapsetIDs:     []string{"a", "b"},
		},
		Script:   "function main() {}",
		EnvItems: []domain.SandboxEnvVar{{Name: "BUG_VALUE", Value: "A"}},
	}
	baseHash := mustSchedulerSandboxConfigHash(t, base)

	for name, mutate := range map[string]func(*domain.Scheduler){
		"workspace":        func(item *domain.Scheduler) { item.Summary.WorkspaceID = "workspace-2" },
		"agent definition": func(item *domain.Scheduler) { item.Summary.AgentID = "agent-2" },
		"driver":           func(item *domain.Scheduler) { item.Summary.Driver = "docker" },
		"guest image":      func(item *domain.Scheduler) { item.Summary.GuestImage = "guest:v2" },
		"default agent":    func(item *domain.Scheduler) { item.Summary.DefaultAgent = "claude" },
		"sandbox policy":   func(item *domain.Scheduler) { item.Summary.SandboxPolicy = domain.SchedulerSandboxPolicyNew },
		"capsets":          func(item *domain.Scheduler) { item.Summary.CapsetIDs = []string{"b"} },
		"environment":      func(item *domain.Scheduler) { item.EnvItems[0].Value = "B" },
		"volumes": func(item *domain.Scheduler) {
			item.Volumes = []domain.VolumeMountSpec{{Type: domain.VolumeMountTypeBind, Source: "/tmp/source", Target: "/workspace/data"}}
		},
		"managed identity": func(item *domain.Scheduler) {
			item.Summary.ProjectID = "project-1"
			item.Summary.AgentName = "worker"
			item.Summary.ProjectSchedulerID = "scheduler-1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := CloneScheduler(base)
			mutate(&changed)
			if got := mustSchedulerSandboxConfigHash(t, changed); got == baseHash {
				t.Fatalf("sandbox config hash did not change for %s", name)
			}
		})
	}
}

func TestSchedulerSandboxConfigHashIgnoresManagedProjectRevision(t *testing.T) {
	base := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID:                 "scheduler-1",
		DefaultAgent:       "codex",
		SandboxPolicy:      domain.SchedulerSandboxPolicySticky,
		ProjectID:          "project-1",
		ProjectRevision:    1,
		AgentName:          "worker",
		ProjectSchedulerID: "scheduler-1",
	}}
	changed := CloneScheduler(base)
	changed.Summary.ProjectRevision = 2

	if got, want := mustSchedulerSandboxConfigHash(t, changed), mustSchedulerSandboxConfigHash(t, base); got != want {
		t.Fatalf("unrelated managed project revision changed config hash: got %q want %q", got, want)
	}
}

func TestSchedulerSandboxConfigHashIgnoresSchedulingAndOrdering(t *testing.T) {
	base := domain.Scheduler{
		Summary: domain.SchedulerSummary{
			ID:                "scheduler-1",
			Name:              "Scheduler",
			Runtime:           domain.SchedulerRuntimeScheduler,
			DefaultAgent:      "codex",
			SandboxPolicy:     domain.SchedulerSandboxPolicySticky,
			ConcurrencyPolicy: domain.SchedulerConcurrencyPolicySkip,
			CapsetIDs:         []string{"a", "b"},
		},
		Script:   "function main() {}",
		EnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}},
	}
	changed := CloneScheduler(base)
	changed.Summary.Name = "Renamed"
	changed.Summary.Description = "new description"
	changed.Summary.Enabled = !base.Summary.Enabled
	changed.Script = "function main() { return 'new prompt'; }"
	changed.Summary.CapsetIDs = []string{"b", "a", "a"}
	changed.EnvItems = []domain.SandboxEnvVar{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}

	if got, want := mustSchedulerSandboxConfigHash(t, changed), mustSchedulerSandboxConfigHash(t, base); got != want {
		t.Fatalf("non-sandbox update changed config hash: got %q want %q", got, want)
	}
}

func mustSchedulerSandboxConfigHash(t *testing.T, scheduler domain.Scheduler) string {
	t.Helper()
	hash, err := SchedulerSandboxConfigHash(scheduler)
	if err != nil {
		t.Fatalf("SchedulerSandboxConfigHash returned error: %v", err)
	}
	return hash
}
