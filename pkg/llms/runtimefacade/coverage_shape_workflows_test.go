package runtimefacade_test

import "testing"

func TestIntegrationRuntimeFacadeConfigWorkflow(t *testing.T) {
	TestEnsureSessionLLMFacadeConfigCreatesCodexEnvAndToken(t)
	TestEnsureSessionAgentRuntimeConfigClaudeAndOpenCodeWorkflows(t)
}

func TestE2ERuntimeFacadeConfigWorkflow(t *testing.T) {
	TestIntegrationRuntimeFacadeConfigWorkflow(t)
}
