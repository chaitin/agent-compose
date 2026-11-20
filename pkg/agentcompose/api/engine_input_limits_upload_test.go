package api

import (
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func TestEngineInputLimitsReportUploadSource(t *testing.T) {
	config := &appconfig.Config{
		RuntimeDriver:                  appconfig.RuntimeDriverDocker,
		AgentInputUploadFileLimitBytes: 100 << 20,
		AgentInputUploadTurnLimitBytes: 200 << 20,
	}
	limits := engineInputLimitsV2(config)
	if limits.GetMaxUploadFileBytes() != 100<<20 || limits.GetMaxUploadTurnBytes() != 200<<20 || limits.GetMaxUploadChunkBytes() != MaxRunInputUploadChunkBytes {
		t.Fatalf("limits = %+v, want upload limits reported", limits)
	}

	config.RuntimeDriver = appconfig.RuntimeDriverK8s
	limits = engineInputLimitsV2(config)
	if limits.GetMaxUploadFileBytes() != 0 || limits.GetMaxUploadTurnBytes() != 0 || limits.GetMaxUploadChunkBytes() != 0 {
		t.Fatalf("k8s limits = %+v, want the upload source unavailable", limits)
	}
	if limits.GetMaxFileBytes() == 0 {
		t.Fatal("inline limits must still be reported on k8s")
	}
}
