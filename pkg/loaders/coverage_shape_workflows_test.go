package loaders

import "testing"

func TestIntegrationLoaderEngineWorkflow(t *testing.T) {
	testLoaderEngineExecuteSupportsSessionRPCBindings(t)
	testLoaderEngineExecuteSupportsAgentAndLLMBindings(t)
	testLoaderEngineExecuteSupportsCommandBindings(t)
	TestLoaderEngineJSONAndRegistrationBranches(t)
}

func TestE2ELoaderEngineWorkflow(t *testing.T) {
	testLoaderEngineExecuteSupportsSessionRPCBindings(t)
	testLoaderEngineExecuteSupportsAgentAndLLMBindings(t)
	testLoaderEngineExecuteSupportsCommandBindings(t)
	TestLoaderEngineJSONAndRegistrationBranches(t)
}
