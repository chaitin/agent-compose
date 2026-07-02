package capabilities

import "testing"

func TestIntegrationCapabilityServiceWorkflow(t *testing.T) {
	TestCapabilityServiceStatusDoesNotExposeAddr(t)
	TestCapabilityServiceStatusReportsRuntimeProxyConfig(t)
	TestCapabilityServiceCatalog(t)
}

func TestE2ECapabilityServiceWorkflow(t *testing.T) {
	TestCapabilityServiceStatusDoesNotExposeAddr(t)
	TestCapabilityServiceStatusReportsRuntimeProxyConfig(t)
	TestCapabilityServiceCatalog(t)
}
