package projects

import (
	"reflect"
	"testing"

	domain "agent-compose/pkg/model"
)

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
