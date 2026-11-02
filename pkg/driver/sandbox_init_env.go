package driver

import (
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// sandboxInitEgressEnv returns the egress mode to place in a sandbox's
// environment when init egress observation is configured. It returns false for
// the default off mode: the init binary defaults to off and a legacy init
// ignores the variable, so there is nothing to inject.
func sandboxInitEgressEnv(config *appconfig.Config) (string, bool) {
	if config == nil {
		return "", false
	}
	mode := strings.TrimSpace(config.SandboxInitEgress)
	if mode == "" || mode == string(sandboxinit.EgressModeOff) {
		return "", false
	}
	return mode, true
}
