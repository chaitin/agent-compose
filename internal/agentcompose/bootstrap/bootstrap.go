package bootstrap

import (
	"errors"
	"log/slog"
	"sync"

	"github.com/samber/do/v2"
)

// Hooks contains the package-specific operations needed to assemble the
// agent-compose service graph without making this bootstrap package depend on
// concrete service implementations.
type Hooks struct {
	Register        func(do.Injector)
	StartBackground func(do.Injector) error
}

var (
	mu    sync.RWMutex
	hooks Hooks
)

// Configure installs the concrete bootstrap hooks. It is intended to be called
// from the service package during initialization.
func Configure(next Hooks) {
	mu.Lock()
	defer mu.Unlock()
	hooks = next
}

func currentHooks() Hooks {
	mu.RLock()
	defer mu.RUnlock()
	return hooks
}

// Setup registers routes and starts background managers.
func Setup(di do.Injector) {
	Register(di)
	if err := StartBackground(di); err != nil {
		slog.Error("failed to start agent-compose background managers", "error", err)
	}
}

// Register registers constructors and routes for the agent-compose service.
func Register(di do.Injector) {
	h := currentHooks()
	if h.Register == nil {
		panic("agent-compose bootstrap register hook is not configured")
	}
	h.Register(di)
}

// StartBackground starts background managers for the agent-compose service.
func StartBackground(di do.Injector) error {
	h := currentHooks()
	if h.StartBackground == nil {
		return errors.New("agent-compose bootstrap start background hook is not configured")
	}
	return h.StartBackground(di)
}
