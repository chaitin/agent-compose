package bus

import "testing"

func TestIntegrationLoaderBusWorkflow(t *testing.T) {
	TestLoaderBusPublishReportsFullChannel(t)
}

func TestE2ELoaderBusWorkflow(t *testing.T) {
	TestLoaderBusPublishReportsFullChannel(t)
}
