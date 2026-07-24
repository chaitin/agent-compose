package projects_test

import "testing"

func TestE2ELegacyDefaultProjectMigrationWorkflows(t *testing.T) {
	t.Run("legacy resources migrate into the v2 default project", TestIntegrationLegacyDefaultProjectAdoptsLoaderHistoryAtCurrentRevision)
	t.Run("legacy loader environment survives project apply", TestIntegrationLegacyLoaderEnvironmentSurvivesProjectApply)
	t.Run("loader file workspaces preserve source and scheduler bindings", TestIntegrationLegacyLoaderFileWorkspacePreservesSourceAndSchedulerBindings)
}
