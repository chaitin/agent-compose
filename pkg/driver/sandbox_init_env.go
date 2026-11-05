package driver

import (
	"fmt"
	"log/slog"
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

// sandboxInitLandlockEnv returns the Landlock configuration to place in a
// sandbox's environment. The fallback only matters when enforce is requested,
// but it is always sent with the mode so init reads one coherent configuration.
func sandboxInitLandlockEnv(config *appconfig.Config) (map[string]string, bool) {
	if config == nil {
		return nil, false
	}
	mode := strings.TrimSpace(config.SandboxInitLandlock)
	if mode == "" || mode == string(sandboxinit.LandlockModeOff) {
		return nil, false
	}
	env := map[string]string{sandboxinit.LandlockModeEnvVar: mode}
	fallback := strings.TrimSpace(config.SandboxInitLandlockFallback)
	if fallback != "" {
		env[sandboxinit.LandlockFallbackEnvVar] = fallback
	}
	return env, true
}

// sandboxInitIsolationEnv returns every init isolation setting that travels
// through the sandbox environment. Drivers set the returned entries on the
// sandbox container; a legacy init ignores them.
func sandboxInitIsolationEnv(config *appconfig.Config) map[string]string {
	env := map[string]string{}
	if egress, ok := sandboxInitEgressEnv(config); ok {
		env[sandboxinit.EgressModeEnvVar] = egress
	}
	if landlock, ok := sandboxInitLandlockEnv(config); ok {
		for key, value := range landlock {
			env[key] = value
		}
	}
	return env
}

// requireSandboxInitLandlockCapability makes the ready handshake prove the
// confinement the daemon asked for. A guest image that predates Landlock, or a
// kernel that cannot enforce it, does not advertise the capability; with the
// default fail fallback the sandbox must not start and report confinement it
// does not have. The explicit warn fallback logs the degradation instead.
func requireSandboxInitLandlockCapability(info sandboxinit.ServerInfo, config *appconfig.Config) error {
	if config == nil {
		return nil
	}
	mode, err := sandboxinit.ParseLandlockMode(config.SandboxInitLandlock)
	if err != nil {
		return fmt.Errorf("sandbox init Landlock configuration is invalid: %w", err)
	}
	if mode != sandboxinit.LandlockModeEnforce {
		return nil
	}
	fallback, err := sandboxinit.ParseLandlockFallback(config.SandboxInitLandlockFallback)
	if err != nil {
		return fmt.Errorf("sandbox init Landlock fallback configuration is invalid: %w", err)
	}
	if info.Supports(sandboxinit.CapabilityLandlockEnforce) {
		return nil
	}
	if fallback == sandboxinit.LandlockFallbackFail {
		return fmt.Errorf("sandbox init did not advertise %q although SANDBOX_INIT_LANDLOCK=enforce; the guest cannot enforce the requested filesystem confinement (capabilities: %v)", sandboxinit.CapabilityLandlockEnforce, info.Capabilities)
	}
	slog.Warn("sandbox init Landlock enforcement was requested but the guest cannot provide it; the sandbox starts without filesystem confinement",
		"fallback", string(fallback), "capabilities", info.Capabilities)
	return nil
}
