package driver

import (
	"fmt"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// sandboxInitEgressRequiresPtrace reports whether the init process needs
// CAP_SYS_PTRACE. Mediation duplicates the workload socket with pidfd_getfd,
// which Docker's default seccomp profile allows only with that capability.
func sandboxInitEgressRequiresPtrace(config *appconfig.Config) bool {
	if config == nil {
		return false
	}
	return strings.TrimSpace(config.SandboxInitEgress) == string(sandboxinit.EgressModeEnforce)
}

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

// requireSandboxInitEgressCapabilities turns a requested enforce mode into a
// start-time contract: the init that actually became ready must have installed
// mediation and hardening. A guest kernel without seccomp user-notification, an
// older guest image, or a missing proxy credential makes init fall back to off;
// accepting that sandbox would report enforcement the runtime does not have.
func requireSandboxInitEgressCapabilities(info sandboxinit.ServerInfo, config *appconfig.Config, policy *SandboxNetworkPolicy) error {
	if !sandboxInitEgressRequiresPtrace(config) || policy == nil || !policy.Restricts() || !policy.MediatedByForwardProxy {
		return nil
	}
	for _, capability := range []string{
		sandboxinit.CapabilityEgressMediate,
		sandboxinit.CapabilityEgressHardening,
	} {
		if !info.Supports(capability) {
			return fmt.Errorf("sandbox init did not advertise %q although SANDBOX_INIT_EGRESS=enforce; the guest cannot enforce the requested egress policy (capabilities: %v)", capability, info.Capabilities)
		}
	}
	return nil
}
