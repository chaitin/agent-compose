package projects

import (
	"reflect"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestMergeManagedLoaderOverridePreservesLegacyTaskStateAndManagedAgentBinding(t *testing.T) {
	createdAt := time.Unix(100, 0)
	updatedAt := time.Unix(200, 0)
	latestRunAt := time.Unix(300, 0)
	current := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:      "compiled-loader",
			AgentID: "managed-agent",
		},
		Triggers: []domain.LoaderTrigger{{ID: "trigger-1", LoaderID: "compiled-loader", Enabled: true}},
	}
	override := legacyManagedLoaderOverride{Loader: domain.Loader{
		Summary: domain.LoaderSummary{
			ID:          "legacy-loader",
			AgentID:     "legacy-agent",
			WorkspaceID: "legacy-workspace",
			CreatedAt:   createdAt,
			UpdatedAt:   updatedAt,
			LastError:   "legacy-error",
			RunCount:    7,
			EventCount:  11,
			LatestRunAt: latestRunAt,
		},
		Triggers: []domain.LoaderTrigger{{
			ID:          "trigger-1",
			LoaderID:    "legacy-loader",
			Enabled:     false,
			NextFireAt:  time.Unix(400, 0),
			LastFiredAt: time.Unix(500, 0),
		}},
	}}

	got := mergeManagedLoaderOverride(current, override)

	if got.Summary.ID != "legacy-loader" || got.Summary.AgentID != "managed-agent" {
		t.Fatalf("adopted loader identity/binding = %#v", got.Summary)
	}
	if got.Summary.WorkspaceID != "legacy-workspace" ||
		got.Summary.CreatedAt != createdAt ||
		got.Summary.UpdatedAt != updatedAt ||
		got.Summary.LastError != "legacy-error" ||
		got.Summary.RunCount != 7 ||
		got.Summary.EventCount != 11 ||
		got.Summary.LatestRunAt != latestRunAt {
		t.Fatalf("legacy task state was not preserved: %#v", got.Summary)
	}
	if len(got.Triggers) != 1 ||
		got.Triggers[0].LoaderID != "legacy-loader" ||
		got.Triggers[0].Enabled ||
		got.Triggers[0].NextFireAt != override.Loader.Triggers[0].NextFireAt ||
		got.Triggers[0].LastFiredAt != override.Loader.Triggers[0].LastFiredAt {
		t.Fatalf("legacy trigger state was not preserved: %#v", got.Triggers)
	}
	if current.Summary.ID != "compiled-loader" || current.Summary.AgentID != "managed-agent" {
		t.Fatalf("compiled loader was mutated: %#v", current.Summary)
	}
}

func TestMergeLegacyManagedLoaderEnv(t *testing.T) {
	tests := []struct {
		name            string
		candidate       []domain.SandboxEnvVar
		loader          []domain.SandboxEnvVar
		baseline        []domain.SandboxEnvVar
		baselineKnown   bool
		wantOverlay     []domain.SandboxEnvVar
		wantEffective   []domain.SandboxEnvVar
		absentEffective []string
	}{
		{
			name:      "initial migration keeps the complete legacy loader layer",
			candidate: []domain.SandboxEnvVar{{Name: "SHARED", Value: "agent"}},
			loader: []domain.SandboxEnvVar{
				{Name: "LOADER_ONLY", Value: "loader-only"},
				{Name: "SHARED", Value: "loader", Secret: true},
			},
			wantOverlay: []domain.SandboxEnvVar{
				{Name: "LOADER_ONLY", Value: "loader-only"},
				{Name: "SHARED", Value: "loader", Secret: true},
			},
			wantEffective: []domain.SandboxEnvVar{
				{Name: "LOADER_ONLY", Value: "loader-only"},
				{Name: "SHARED", Value: "loader", Secret: true},
			},
		},
		{
			name:          "unchanged project env keeps out of band loader edits and precedence",
			baselineKnown: true,
			baseline: []domain.SandboxEnvVar{
				{Name: "AGENT_ONLY", Value: "agent-only"},
				{Name: "SHARED", Value: "agent"},
			},
			candidate: []domain.SandboxEnvVar{
				{Name: "AGENT_ONLY", Value: "agent-only"},
				{Name: "SHARED", Value: "agent"},
			},
			loader: []domain.SandboxEnvVar{
				{Name: "LOADER_ONLY", Value: "manual"},
				{Name: "SHARED", Value: "manual-shared", Secret: true},
			},
			wantOverlay: []domain.SandboxEnvVar{
				{Name: "LOADER_ONLY", Value: "manual"},
				{Name: "SHARED", Value: "manual-shared", Secret: true},
			},
			wantEffective: []domain.SandboxEnvVar{
				{Name: "AGENT_ONLY", Value: "agent-only"},
				{Name: "LOADER_ONLY", Value: "manual"},
				{Name: "SHARED", Value: "manual-shared", Secret: true},
			},
		},
		{
			name:          "explicit project changes remove only conflicting loader overrides",
			baselineKnown: true,
			baseline: []domain.SandboxEnvVar{
				{Name: "REMOVE_ME", Value: "old"},
				{Name: "SECRET_FLAG", Value: "same"},
				{Name: "SHARED", Value: "agent-old"},
			},
			candidate: []domain.SandboxEnvVar{
				{Name: "LOADER_ADDED", Value: "agent-claims", Secret: true},
				{Name: "SECRET_FLAG", Value: "same", Secret: true},
				{Name: "SHARED", Value: "agent-new"},
			},
			loader: []domain.SandboxEnvVar{
				{Name: "LOADER_ADDED", Value: "manual-added"},
				{Name: "LOADER_STAYS", Value: "manual-stays"},
				{Name: "REMOVE_ME", Value: "manual-remove"},
				{Name: "SECRET_FLAG", Value: "manual-secret"},
				{Name: "SHARED", Value: "manual-shared"},
			},
			wantOverlay: []domain.SandboxEnvVar{
				{Name: "LOADER_STAYS", Value: "manual-stays"},
			},
			wantEffective: []domain.SandboxEnvVar{
				{Name: "LOADER_ADDED", Value: "agent-claims", Secret: true},
				{Name: "LOADER_STAYS", Value: "manual-stays"},
				{Name: "SECRET_FLAG", Value: "same", Secret: true},
				{Name: "SHARED", Value: "agent-new"},
			},
			absentEffective: []string{"REMOVE_ME"},
		},
		{
			name:          "unrelated project env change leaves loader only keys alone",
			baselineKnown: true,
			baseline:      []domain.SandboxEnvVar{{Name: "AGENT_ONLY", Value: "old"}},
			candidate:     []domain.SandboxEnvVar{{Name: "AGENT_ONLY", Value: "new"}},
			loader:        []domain.SandboxEnvVar{{Name: "LOADER_ONLY", Value: "manual", Secret: true}},
			wantOverlay:   []domain.SandboxEnvVar{{Name: "LOADER_ONLY", Value: "manual", Secret: true}},
			wantEffective: []domain.SandboxEnvVar{
				{Name: "AGENT_ONLY", Value: "new"},
				{Name: "LOADER_ONLY", Value: "manual", Secret: true},
			},
		},
		{
			name:            "out of band loader deletion stays deleted",
			baselineKnown:   true,
			baseline:        []domain.SandboxEnvVar{{Name: "AGENT_ONLY", Value: "same"}},
			candidate:       []domain.SandboxEnvVar{{Name: "AGENT_ONLY", Value: "same"}},
			wantEffective:   []domain.SandboxEnvVar{{Name: "AGENT_ONLY", Value: "same"}},
			absentEffective: []string{"LOADER_ONLY"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateBefore := append([]domain.SandboxEnvVar(nil), test.candidate...)
			loaderBefore := append([]domain.SandboxEnvVar(nil), test.loader...)
			baselineBefore := append([]domain.SandboxEnvVar(nil), test.baseline...)
			override := legacyManagedLoaderOverride{
				Loader:           domain.Loader{EnvItems: test.loader},
				BaselineAgentEnv: test.baseline,
				BaselineKnown:    test.baselineKnown,
			}

			got := mergeLegacyManagedLoaderEnv(test.candidate, override)
			if !SameSandboxEnvItems(got, test.wantOverlay) {
				t.Fatalf("overlay = %#v, want %#v", got, test.wantOverlay)
			}
			effective := domain.MergeEnvItems(test.candidate, got)
			if !SameSandboxEnvItems(effective, test.wantEffective) {
				t.Fatalf("effective env = %#v, want %#v", effective, test.wantEffective)
			}
			effectiveByName := sandboxEnvItemsByName(effective)
			for _, name := range test.absentEffective {
				if _, exists := effectiveByName[name]; exists {
					t.Fatalf("effective env unexpectedly contains %s: %#v", name, effectiveByName[name])
				}
			}

			if !reflect.DeepEqual(test.candidate, candidateBefore) {
				t.Fatalf("candidate env mutated: got %#v want %#v", test.candidate, candidateBefore)
			}
			if !reflect.DeepEqual(test.loader, loaderBefore) {
				t.Fatalf("loader env mutated: got %#v want %#v", test.loader, loaderBefore)
			}
			if !reflect.DeepEqual(test.baseline, baselineBefore) {
				t.Fatalf("baseline env mutated: got %#v want %#v", test.baseline, baselineBefore)
			}
		})
	}
}
