package app

import (
	"log/slog"
	"path/filepath"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/runinputs"
)

// runInputStore holds the UploadRunInput staging area. Store is nil when the
// daemon cannot accept the upload source (the default driver is K8s), so the
// RPC, run admission, and cleanup all see the same "unavailable" answer that
// EngineInputLimits reports.
type runInputStore struct {
	Store *runinputs.Store
}

// NewRunInputStore opens the staging area under DATA_ROOT when uploads are
// available.
func NewRunInputStore(di do.Injector) (runInputStore, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	if config.AgentInputLimits().MaxUploadFileBytes == 0 {
		return runInputStore{}, nil
	}
	store, err := runinputs.Open(runinputs.Options{
		Root:           filepath.Join(config.DataRoot, "run-inputs"),
		MaxObjectBytes: config.AgentInputUploadFileLimitBytes,
		MaxStoreBytes:  config.AgentInputUploadStoreLimitBytes,
		TTL:            config.AgentInputUploadTTL,
		Logger:         do.MustInvoke[*slog.Logger](di),
	})
	if err != nil {
		return runInputStore{}, err
	}
	return runInputStore{Store: store}, nil
}
