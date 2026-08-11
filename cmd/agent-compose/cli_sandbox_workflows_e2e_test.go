package main

import "testing"

func TestE2ECLISandboxNamingUserWorkflows(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T)
	}{
		{
			name: "run --sandbox",
			run:  TestIntegrationCLIRunStreamsOutputAndSupportsSandboxReuse,
		},
		{
			name: "ps --json sandbox shape",
			run:  TestIntegrationCLIPSTableAndJSON,
		},
		{
			name: "exec <sandbox> --command",
			run:  TestIntegrationCLIStreamExecsAndSupportsJSON,
		},
		{
			name: "logs --sandbox",
			run:  TestIntegrationCLILogsFiltersRunAgentSessionAndJSON,
		},
		{
			name: "inspect sandbox and reject removed inspect session",
			run:  TestIntegrationCLIInspectProjectAgentRunSandboxSessionJSON,
		},
		{
			name: "sandbox stop",
			run:  TestIntegrationCLIStopSandbox,
		},
		{
			name: "sandbox resume",
			run:  TestIntegrationCLIResumeSandboxesJSON,
		},
		{
			name: "sandbox rm",
			run:  TestIntegrationCLIRemoveSandboxes,
		},
		{
			name: "sandbox prune dry-run",
			run:  TestIntegrationCLISandboxPruneDryRunFiltersAndSafety,
		},
		{
			name: "sandbox prune force",
			run:  TestIntegrationCLISandboxPruneForceRemovesMatchedAndReportsSkipped,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}
