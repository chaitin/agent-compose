package app

import (
	"log/slog"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

func runInputStoreFor(t *testing.T, driver string) runInputStore {
	t.Helper()
	di := do.New()
	do.ProvideValue(di, &appconfig.Config{
		RuntimeDriver:                   driver,
		DataRoot:                        t.TempDir(),
		AgentInputUploadFileLimitBytes:  1 << 20,
		AgentInputUploadTurnLimitBytes:  2 << 20,
		AgentInputUploadStoreLimitBytes: 8 << 20,
		AgentInputUploadTTL:             time.Minute,
	})
	do.ProvideValue(di, slog.Default())
	store, err := NewRunInputStore(di)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNewRunInputStoreFollowsReportedUploadLimits(t *testing.T) {
	if runInputStoreFor(t, appconfig.RuntimeDriverDocker).Store == nil {
		t.Fatal("docker daemon has no staging area")
	}
	// K8s reports zero upload limits, so the RPC must be unavailable too.
	if runInputStoreFor(t, appconfig.RuntimeDriverK8s).Store != nil {
		t.Fatal("k8s daemon opened a staging area it cannot deliver from")
	}
	if runUploadResolver(nil) != nil {
		t.Fatal("a nil store must stay a nil resolver")
	}
}
