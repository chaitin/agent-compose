package driver

import containerapi "github.com/docker/docker/api/types/container"

// dockerSandboxNetworkMode selects the HostConfig.NetworkMode for a sandbox.
//
// A declared default-deny policy selects Docker's "none" network, which gives
// the container a network namespace with only a loopback interface: real outer
// denial, but all-or-nothing. The declared allowances and the engine-owned
// endpoints are deliberately not applied, because expressing them needs a
// per-sandbox network namespace plus a connect(2)/L7 mediator, and the base
// compose deployment does not grant the daemon NET_ADMIN or root. The strength
// report states that limit instead of implying the allowance list takes effect.
//
// Every other policy keeps the topology network, so an undeclared policy is
// byte-for-byte today's behavior (D3).
func dockerSandboxNetworkMode(base containerapi.NetworkMode, policy *SandboxNetworkPolicy) containerapi.NetworkMode {
	if policy == nil || !policy.DenyByDefault() {
		return base
	}
	return containerapi.NetworkMode("none")
}
