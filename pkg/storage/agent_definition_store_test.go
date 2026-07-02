package storage

import (
	"context"
	"strings"
	"testing"
)

func TestAgentDefinitionConfigStoreCRUDAndWorkspaceProtection(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	workspace, err := store.CreateWorkspaceConfig(ctx, WorkspaceConfig{
		Name:       "Agent Files",
		Type:       "git",
		ConfigJSON: `{"repo_url":"https://example.com/repo.git","branch":"main"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	created, err := store.CreateAgentDefinition(ctx, AgentDefinition{
		ID:          "agent-1",
		Name:        " Agent One ",
		Enabled:     true,
		WorkspaceID: workspace.ID,
		EnvItems: []SessionEnvVar{
			{Name: " B ", Value: "2"},
			{Name: "A", Value: "1"},
			{Name: "B", Value: "3"},
			{Name: " ", Value: "skip"},
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	if created.Provider != "codex" || created.ConfigJSON != "{}" {
		t.Fatalf("defaults = provider %q config %q", created.Provider, created.ConfigJSON)
	}
	if len(created.EnvItems) != 2 || created.EnvItems[0].Name != "A" || created.EnvItems[1].Value != "3" {
		t.Fatalf("env items = %#v", created.EnvItems)
	}
	if err := store.DeleteWorkspaceConfig(ctx, workspace.ID); err == nil || !strings.Contains(err.Error(), "referenced by") {
		t.Fatalf("DeleteWorkspaceConfig error = %v, want referenced by", err)
	}
	listed, err := store.ListAgentDefinitions(ctx, AgentDefinitionListOptions{IncludeDisabled: true})
	if err != nil {
		t.Fatalf("ListAgentDefinitions returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Agents) != 1 {
		t.Fatalf("listed = %#v", listed)
	}
	updated := listed.Agents[0]
	updated.Name = "Agent Renamed"
	updated.Enabled = false
	saved, err := store.UpdateAgentDefinition(ctx, updated)
	if err != nil {
		t.Fatalf("UpdateAgentDefinition returned error: %v", err)
	}
	if saved.CreatedAt.IsZero() || !saved.UpdatedAt.After(saved.CreatedAt) {
		t.Fatalf("timestamps after update = created %s updated %s", saved.CreatedAt, saved.UpdatedAt)
	}
	enabled, err := store.SetAgentDefinitionEnabled(ctx, saved.ID, true)
	if err != nil {
		t.Fatalf("SetAgentDefinitionEnabled returned error: %v", err)
	}
	if !enabled.Enabled {
		t.Fatalf("enabled flag false")
	}
	if err := store.DeleteAgentDefinition(ctx, enabled.ID); err != nil {
		t.Fatalf("DeleteAgentDefinition returned error: %v", err)
	}
	if _, err := store.GetAgentDefinition(ctx, enabled.ID); err == nil {
		t.Fatalf("expected deleted agent get to fail")
	}
	if err := store.DeleteWorkspaceConfig(ctx, workspace.ID); err != nil {
		t.Fatalf("DeleteWorkspaceConfig after agent delete returned error: %v", err)
	}
}
