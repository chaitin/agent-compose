package loaders

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

func TestLoaderSandboxConfigHashTracksSandboxSemantics(t *testing.T) {
	base := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:            "loader-1",
			Name:          "Loader",
			Runtime:       domain.LoaderRuntimeScheduler,
			DefaultAgent:  "codex",
			SandboxPolicy: domain.LoaderSandboxPolicySticky,
			CapsetIDs:     []string{"a", "b"},
		},
		Script:   "function main() {}",
		EnvItems: []domain.SandboxEnvVar{{Name: "BUG_VALUE", Value: "A"}},
	}
	baseHash := mustLoaderSandboxConfigHash(t, base)

	for name, mutate := range map[string]func(*domain.Loader){
		"workspace":        func(item *domain.Loader) { item.Summary.WorkspaceID = "workspace-2" },
		"agent definition": func(item *domain.Loader) { item.Summary.AgentID = "agent-2" },
		"driver":           func(item *domain.Loader) { item.Summary.Driver = "docker" },
		"guest image":      func(item *domain.Loader) { item.Summary.GuestImage = "guest:v2" },
		"default agent":    func(item *domain.Loader) { item.Summary.DefaultAgent = "claude" },
		"sandbox policy":   func(item *domain.Loader) { item.Summary.SandboxPolicy = domain.LoaderSandboxPolicyNew },
		"capsets":          func(item *domain.Loader) { item.Summary.CapsetIDs = []string{"b"} },
		"environment":      func(item *domain.Loader) { item.EnvItems[0].Value = "B" },
		"volumes": func(item *domain.Loader) {
			item.Volumes = []domain.VolumeMountSpec{{Type: domain.VolumeMountTypeBind, Source: "/tmp/source", Target: "/workspace/data"}}
		},
		"managed revision": func(item *domain.Loader) {
			item.Summary.ManagedProjectID = "project-1"
			item.Summary.ManagedAgentName = "worker"
			item.Summary.ManagedSchedulerID = "scheduler-1"
			item.Summary.ManagedRevision = 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := CloneLoader(base)
			mutate(&changed)
			if got := mustLoaderSandboxConfigHash(t, changed); got == baseHash {
				t.Fatalf("sandbox config hash did not change for %s", name)
			}
		})
	}
}

func TestLoaderSandboxConfigHashIgnoresSchedulingAndOrdering(t *testing.T) {
	base := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:                "loader-1",
			Name:              "Loader",
			Runtime:           domain.LoaderRuntimeScheduler,
			DefaultAgent:      "codex",
			SandboxPolicy:     domain.LoaderSandboxPolicySticky,
			ConcurrencyPolicy: domain.LoaderConcurrencyPolicySkip,
			CapsetIDs:         []string{"a", "b"},
		},
		Script:   "function main() {}",
		EnvItems: []domain.SandboxEnvVar{{Name: "A", Value: "1"}, {Name: "B", Value: "2"}},
	}
	changed := CloneLoader(base)
	changed.Summary.Name = "Renamed"
	changed.Summary.Description = "new description"
	changed.Summary.Enabled = !base.Summary.Enabled
	changed.Script = "function main() { return 'new prompt'; }"
	changed.Summary.CapsetIDs = []string{"b", "a", "a"}
	changed.EnvItems = []domain.SandboxEnvVar{{Name: "B", Value: "2"}, {Name: "A", Value: "1"}}

	if got, want := mustLoaderSandboxConfigHash(t, changed), mustLoaderSandboxConfigHash(t, base); got != want {
		t.Fatalf("non-sandbox update changed config hash: got %q want %q", got, want)
	}
}

func mustLoaderSandboxConfigHash(t *testing.T, loader domain.Loader) string {
	t.Helper()
	hash, err := LoaderSandboxConfigHash(loader)
	if err != nil {
		t.Fatalf("LoaderSandboxConfigHash returned error: %v", err)
	}
	return hash
}
