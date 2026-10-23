package driver

import (
	"errors"
	"fmt"
	"log/slog"

	config "github.com/chaitin/agent-compose/pkg/config"
)

// This file owns the honest strength of sandbox egress enforcement per driver
// and the gate that keeps a declared restricting policy from silently
// degrading to unrestricted egress: it either reports the degradation or, when
// the deployment configures it, refuses the sandbox.
//
// The strength is a driver-local descriptor rather than a pkg/capmatrix type
// because pkg/driver cannot import pkg/capmatrix (pkg/llms imports pkg/driver).
// pkg/capmatrix consumes it through the egress_policy dimension's Mechanism and
// Observed fields, which is the seam SEC-3 wires into the runtime API.

// SandboxEgressStrength is how strongly a driver enforces a declared egress
// policy. It is reported alongside the driver capability facts, never assumed.
type SandboxEgressStrength string

const (
	// SandboxEgressStrengthNone means the driver applies no part of a declared
	// policy; egress stays as open as it is without one.
	SandboxEgressStrengthNone SandboxEgressStrength = "none"
	// SandboxEgressStrengthAllowList means the driver applies default deny plus
	// the ordered allowance list, including the engine-owned endpoints.
	SandboxEgressStrengthAllowList SandboxEgressStrength = "allowlist"
	// SandboxEgressStrengthDenyList means the driver applies an allow default
	// plus the ordered denial list, with the engine-owned endpoints ahead of it.
	SandboxEgressStrengthDenyList SandboxEgressStrength = "denylist"
	// mechanismForwardProxy names the engine-side mediation: the daemon decides
	// every connection in its own forward proxy, so the strength does not come
	// from a driver feature.
	mechanismForwardProxy = "forward_proxy"
)

// SandboxNetworkEnforcement describes one runtime driver's egress enforcement.
type SandboxNetworkEnforcement struct {
	Driver    string
	Strength  SandboxEgressStrength
	Mechanism string
	// AppliesAllowEntries reports whether declared allowances reach the driver.
	AppliesAllowEntries bool
	// AppliesDenyEntries reports whether declared denials reach the driver.
	AppliesDenyEntries bool
	// AppliesEngineEndpoints reports whether the engine-owned endpoints stay
	// reachable when the policy restricts.
	AppliesEngineEndpoints bool
	// AppliesDenyDomains reports whether the declared deny domains reach the
	// driver's DNS resolution path.
	AppliesDenyDomains bool
	// AppliesAllowedAddresses reports whether this mechanism can constrain which
	// addresses a declared name may resolve to. Only a mediator that resolves the
	// destination itself can, so a driver that pushes host patterns to a runtime
	// leaves that part of a declaration unapplied rather than applying it
	// partially without saying so.
	AppliesAllowedAddresses bool
	// Notes is the human-readable limit of what the driver applies, so a report
	// never presents a partial mechanism as complete.
	Notes string
}

// SandboxNetworkEnforcementFor returns the enforcement declaration for one
// runtime driver. It is pure and never probes a runtime.
func SandboxNetworkEnforcementFor(driver string) SandboxNetworkEnforcement {
	switch resolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return SandboxNetworkEnforcement{
			Driver:                 RuntimeDriverMicrosandbox,
			Strength:               SandboxEgressStrengthAllowList,
			Mechanism:              mechanismSDKNetworkPolicy,
			AppliesAllowEntries:    true,
			AppliesDenyEntries:     true,
			AppliesEngineEndpoints: true,
			AppliesDenyDomains:     true,
			// The declaration is what the driver asks the SDK to install. A real KVM
			// run confirmed the deny default and the allow rules (a name without an
			// allowance fails to resolve, and an address allowance is refused on
			// every other address and port) and showed a limit the note records: an
			// allowed HTTPS destination did not complete a TLS handshake there, so
			// the engine's proxy is what carries an HTTPS allowance today.
			Notes: "verified on a real KVM run: the SDK NetworkConfig applies the deny egress default and the allow rules (a name without an allowance fails to resolve, and an address allowance is refused on every other address and port), but it matches the name the sandbox asked for and does not constrain which addresses that name may resolve to; an allowed HTTPS destination did not complete a TLS handshake in that run, so the engine's forward proxy is what carries an HTTPS allowance when the sandbox can reach the daemon; the mechanism's own refusals are not recorded by the daemon",
		}
	case RuntimeDriverK8s:
		// A per-sandbox egress NetworkPolicy would deny all egress, including
		// the engine's OWN LLM facade and telemetry endpoints: NetworkPolicy is
		// L3/L4 and cannot express the declared allowances or those endpoints,
		// and the engine cannot even verify that the cluster CNI enforces
		// NetworkPolicy. Applying it would produce a healthy-looking sandbox
		// that can never call its model, so the engine refuses instead.
		return SandboxNetworkEnforcement{
			Driver:                 RuntimeDriverK8s,
			Strength:               SandboxEgressStrengthNone,
			Mechanism:              reasonNotConfigured,
			AppliesAllowEntries:    false,
			AppliesEngineEndpoints: false,
			AppliesDenyDomains:     false,
			Notes:                  "no egress filter is installed by this driver: a per-sandbox egress NetworkPolicy cannot express the declared DNS-named allowances or keep the engine's own LLM facade and telemetry endpoints reachable (NetworkPolicy is L3/L4 and cannot match DNS names), and the engine cannot verify that the cluster CNI would enforce it at all; the engine's forward proxy enforces the declaration instead when the sandbox can reach the daemon",
		}
	case RuntimeDriverDocker:
		// NetworkMode=none is a real outer deny, but it also severs the
		// engine's OWN endpoints: the sandbox reaches the daemon's LLM facade
		// and telemetry over host:port HTTP, and a loopback-only network
		// namespace cannot reach them. Applying it would produce a
		// healthy-looking sandbox that can never call its model, so the engine
		// refuses a declared restriction instead.
		return SandboxNetworkEnforcement{
			Driver:                 RuntimeDriverDocker,
			Strength:               SandboxEgressStrengthNone,
			Mechanism:              reasonNotConfigured,
			AppliesAllowEntries:    false,
			AppliesEngineEndpoints: false,
			AppliesDenyDomains:     false,
			Notes:                  "no egress filter is installed by this driver: NetworkMode=none is a real outer deny but it would also deny the engine's own LLM facade and telemetry endpoints, leaving a healthy-looking sandbox that can never call its model, and the base compose grants neither NET_ADMIN nor root for a per-sandbox netns; the engine's forward proxy enforces the declaration instead when the sandbox can reach the daemon",
		}
	case RuntimeDriverBoxlite:
		return SandboxNetworkEnforcement{
			Driver:                 RuntimeDriverBoxlite,
			Strength:               SandboxEgressStrengthNone,
			Mechanism:              reasonNotConfigured,
			AppliesAllowEntries:    false,
			AppliesEngineEndpoints: false,
			AppliesDenyDomains:     false,
			Notes:                  "the driver sets network_enabled unconditionally and binds neither boxlite_options_add_network_allow nor boxlite_options_set_network_disabled, so a declared deny policy is not applied by the driver until the FFI binding is verified on a real KVM host; the engine's forward proxy enforces the declaration instead when the sandbox can reach the daemon",
		}
	default:
		return SandboxNetworkEnforcement{
			Driver:    resolveRuntimeDriver(driver),
			Strength:  SandboxEgressStrengthNone,
			Mechanism: reasonUnsupported,
			Notes:     "the driver has no egress enforcement surface",
		}
	}
}

// SandboxEgressForwardProxyEnforcement is the enforcement a sandbox gains from
// the engine's own forward proxy rather than from its driver. The decision is
// the engine's and it applies the declared allowances, denials, and
// engine-owned endpoints, so the strength is an allowlist or denylist on every
// driver whose guest reaches the daemon.
//
// The limit is the transport: a process that ignores the proxy environment dials
// the internet directly and is not decided, until transparent capture puts every
// connection through the proxy. The note says so rather than implying the
// declaration covers traffic the proxy never saw.
func SandboxEgressForwardProxyEnforcement(driver string) SandboxNetworkEnforcement {
	return SandboxNetworkEnforcement{
		Driver:                  resolveRuntimeDriver(driver),
		Strength:                SandboxEgressStrengthAllowList,
		Mechanism:               mechanismForwardProxy,
		AppliesAllowEntries:     true,
		AppliesDenyEntries:      true,
		AppliesEngineEndpoints:  true,
		AppliesAllowedAddresses: true,
		Notes:                   "the engine's forward proxy decides every connection the sandbox routes through it: the declared allowances and the engine-owned endpoints are applied in the daemon, the guest is given the proxy as its egress path, and a change to the declaration reaches a running sandbox because the policy is read per request; a process that ignores the proxy environment is not decided until transparent capture is in place",
	}
}

// ErrSandboxNetworkEnforcementUnavailable reports a declared restricting
// policy the selected driver cannot enforce. It is returned instead of
// starting a sandbox with unrestricted egress.
var ErrSandboxNetworkEnforcementUnavailable = errors.New("sandbox network enforcement unavailable")

// egressPolicyFacts derives the egress_policy capability declaration from the
// driver's actual enforcement strength, so the capability report can never
// claim more than the driver applies. pkg/capmatrix consumes these facts; the
// strength string travels in Observed because the capability contract has no
// separate strength field.
func egressPolicyFacts(driver string) RuntimeCapabilityDimensionFacts {
	enforcement := SandboxNetworkEnforcementFor(driver)
	facts := RuntimeCapabilityDimensionFacts{
		Dimension:       dimensionEgressPolicy,
		Observed:        fmt.Sprintf("strength=%s; %s", enforcement.Strength, enforcement.Notes),
		DefaultBehavior: "an undeclared network policy is not deny, so egress stays unrestricted (D3); when a sandbox declares default: deny-all or a deny entry the engine auto-allows its own runtime LLM facade and telemetry endpoints as non-overridable engine-side rules; when the sandbox can reach the daemon the engine enforces the declaration itself in its forward proxy, and the driver's own strength above does not apply; on a driver that cannot apply the declaration and a sandbox that cannot reach the proxy, the sandbox reports the declaration as unenforced and runs, unless the deployment sets EGRESS_UNENFORCEABLE_POLICY=refuse, in which case the sandbox is refused instead",
	}
	if enforcement.Strength == SandboxEgressStrengthNone {
		facts.Mechanism = reasonNotConfigured
		return facts
	}
	facts.Enforced = true
	facts.Mechanism = enforcement.Mechanism
	return facts
}

// RequireSandboxNetworkEnforcement applies the configured policy to a sandbox
// that declares restricting egress on a driver that cannot apply any part of
// it. A nil or non-restricting policy returns nil: an undeclared policy keeps
// today's behavior (D3), so this gate never tightens a sandbox that did not ask
// for it.
//
// A restriction the driver cannot deliver must not become an availability
// failure, so the default (warn) starts the sandbox and reports that the
// declaration is unenforced; refuse is opt-in for deployments that require the
// declaration to be real. Either way the sandbox is never presented as
// constrained when it is not.
func RequireSandboxNetworkEnforcement(driver, sandboxID string, policy *SandboxNetworkPolicy, mode config.EgressUnenforceablePolicy) error {
	if policy == nil {
		return nil
	}
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("sandbox network policy: %w", err)
	}
	if !policy.Restricts() {
		return nil
	}
	driverEnforcement := SandboxNetworkEnforcementFor(driver)
	enforcement := driverEnforcement
	if policy.MediatedByForwardProxy {
		// The engine is in the traffic path, so the declaration is enforced
		// whatever the driver provides on its own.
		enforcement = SandboxEgressForwardProxyEnforcement(driver)
	}
	if enforcement.Strength != SandboxEgressStrengthNone {
		// The mechanism applies the declaration, but possibly not every part of
		// it. A part it does not apply is a sandbox running as if the declaration
		// said less than it did, which is the failure this gate exists to prevent.
		//
		// A mediated sandbox can have a driver mechanism that applies the
		// destinations but not allowed_ips. The proxy still applies the address
		// constraint to traffic it sees, but a process that bypasses the proxy is
		// only subject to the driver's narrower rule, so the declaration is not
		// fully enforced. Report that separately from the no-mediation case below:
		// naming the proxy as the mechanism that lacks the addresses would be
		// false, and silently treating the proxy as complete would hide the gap.
		if constrained := policy.AddressConstrainedAllowances(); constrained > 0 && policy.MediatedByForwardProxy &&
			driverEnforcement.Strength != SandboxEgressStrengthNone && !driverEnforcement.AppliesAllowedAddresses {
			return reportUndeclaredEnforcement(mode, sandboxID, enforcement, egressGapPartial,
				fmt.Sprintf("%d declared allowance(s) constrain the addresses a name may resolve to; %s applies them to proxied traffic, but the %s driver mechanism (%s) does not apply allowed_ips to traffic that bypasses the proxy",
					constrained, enforcement.Mechanism, driverEnforcement.Driver, driverEnforcement.Mechanism))
		}
		if constrained := policy.AddressConstrainedAllowances(); constrained > 0 && !enforcement.AppliesAllowedAddresses {
			return reportUndeclaredEnforcement(mode, sandboxID, enforcement, egressGapPartial,
				fmt.Sprintf("%d declared allowance(s) constrain the addresses a name may resolve to, and %s applies the destinations without them", constrained, enforcement.Mechanism))
		}
		if denied := len(policy.Deny); denied > 0 && !enforcement.AppliesDenyEntries {
			return reportUndeclaredEnforcement(mode, sandboxID, enforcement, egressGapPartial,
				fmt.Sprintf("%d declared denial(s) are not applied by %s", denied, enforcement.Mechanism))
		}
		// A mediated sandbox can still have its deny domains applied by the
		// driver's own in-guest filter, so the gap exists only when neither
		// mechanism delivers them. No declaration carries deny domains yet, so a
		// project cannot reach this guard today; the admission report in
		// network_policy_report.go reads only the declaration and therefore cannot
		// see them, and has to be extended in the same change that makes them
		// declarable.
		if denied := len(policy.DenyDomains); denied > 0 && !enforcement.AppliesDenyDomains && !driverEnforcement.AppliesDenyDomains {
			return reportUndeclaredEnforcement(mode, sandboxID, enforcement, egressGapPartial,
				fmt.Sprintf("%d declared deny domain(s) are not applied by %s or by the %s driver mechanism (%s)", denied, enforcement.Mechanism, driverEnforcement.Driver, driverEnforcement.Mechanism))
		}
		return nil
	}
	return reportUndeclaredEnforcement(mode, sandboxID, enforcement, egressGapUnrestricted, enforcement.Notes)
}

// egressGap is how much of a declaration the effective mechanism fails to
// apply, so a refusal and a warning state the opening that actually exists
// instead of always claiming the widest one.
type egressGap string

const (
	// egressGapUnrestricted means no part of the declaration is applied and the
	// sandbox would leave with unrestricted egress.
	egressGapUnrestricted egressGap = "unrestricted"
	// egressGapPartial means the mechanism applies the declaration's
	// destinations but not a declared restriction on them, so the resulting
	// egress is narrower than unrestricted even though it is wider than asked.
	egressGapPartial egressGap = "partly applied"
)

// reportUndeclaredEnforcement answers a declaration the effective mechanism does
// not fully apply: refuse when the deployment asked for that, and otherwise
// report the gap and let the sandbox run. Both answers name the same reason, so
// the decision a deployment configured is the decision it can see.
func reportUndeclaredEnforcement(mode config.EgressUnenforceablePolicy, sandboxID string, enforcement SandboxNetworkEnforcement, gap egressGap, reason string) error {
	if mode == config.EgressUnenforceableRefuse {
		return fmt.Errorf("%w: driver %q cannot enforce the declared restricting network policy; refusing to start the sandbox with %s (%s)", ErrSandboxNetworkEnforcementUnavailable, enforcement.Driver, gap, reason)
	}
	slog.Warn("declared network policy is not enforced",
		"sandbox_id", sandboxID,
		"driver", enforcement.Driver,
		"declared_default", "deny-all",
		"egress", string(gap),
		"reason", reason,
	)
	return nil
}
