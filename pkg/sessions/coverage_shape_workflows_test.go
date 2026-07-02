package sessions

import "testing"

func TestIntegrationSessionRPCBridgeWorkflow(t *testing.T) {
	testSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t)
}

func TestE2ESessionRPCBridgeWorkflow(t *testing.T) {
	testSessionRPCBridgeCallJSONSupportsAllSessionRPCs(t)
}
