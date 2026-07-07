package loaders_test

import "testing"

func TestIntegrationLoaderRuntimeAndSchedulerWorkflows(t *testing.T) {
	t.Run("run executor lifecycle", TestRunExecutorLifecycleWorkflows)
	t.Run("event dispatcher", TestEventDispatcherWorkflows)
	t.Run("runtime host agent command llm and session rpc", TestRuntimeHostAgentCommandLLMAndSessionRPC)
	t.Run("runtime host project agent path", TestRuntimeHostProjectAgentPath)
	t.Run("schedule model", TestLoaderScheduleModelWorkflows)
}

func TestE2ELoaderRuntimeAndSchedulerWorkflows(t *testing.T) {
	TestIntegrationLoaderRuntimeAndSchedulerWorkflows(t)
}
