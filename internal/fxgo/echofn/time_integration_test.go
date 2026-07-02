package echofn

import "testing"

func TestIntegrationEpochTimeAPIWorkflow(t *testing.T) {
	testEpochTimeAPIEncodesNestedBsonDDateTimeAsFloat(t)
}

func TestE2EEpochTimeAPIWorkflow(t *testing.T) {
	testEpochTimeAPIEncodesNestedBsonDDateTimeAsFloat(t)
}
