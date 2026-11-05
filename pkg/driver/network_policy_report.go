package driver

import (
	"fmt"
	"strings"

	config "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/egress"
)

// This file owns the account of what a project declared about a sandbox's
// outbound network access and which mechanism will actually apply it. It exists
// because admission is the only moment an operator can act on the answer: a
// declaration that the engine will not enforce must be reported when the project
// is accepted, not discovered later from the absence of a restriction.
//
// The report is derived from the same two inputs the creation path uses — the
// declaration (egress.ReportNetworkPolicy compiles it first) and the driver's
// enforcement descriptor, swapping in the forward-proxy descriptor exactly when
// the engine will mediate — so an admission report cannot claim a mechanism the
// seam would not install.
//
// One input is deliberately not shared: RequireSandboxNetworkEnforcement also
// reads the deny domains of the driver-boundary policy, and
// egress.NetworkDeclaration carries none, so this report states the gaps it can
// see. No project can reach the difference today — the compose schema has no
// field for deny domains and every production call builds the policy with none —
// so the two entries agree on every declaration that can be written. Whoever
// makes deny domains declarable must report them here in the same change, which
// is what TestAdmissionReportCannotSeeUnappliedDenyDomains fails on.

// AgentNetworkPolicyReport is the auditable account of one agent's declared
// outbound network policy and the mechanism that will apply it.
type AgentNetworkPolicyReport struct {
	// Agent is the project-local agent name the declaration belongs to.
	Agent string
	// Driver is the resolved runtime driver the report was computed for.
	Driver string
	// Declared is false when neither the agent nor the daemon declared a network
	// policy. Undeclared is not deny (D3): egress stays unrestricted and there
	// is nothing to enforce. A daemon-wide policy sets it true for every agent,
	// including one that declared nothing itself.
	Declared bool
	// Restricts is false when the declaration cannot refuse anything: an allow
	// default with no denial. It is true for a deny default or any declared
	// denial, and false for an undeclared policy.
	Restricts bool
	// Default is the action applied to traffic no entry matches.
	Default egress.Action
	// Allow lists the declared allowances, including their address ranges.
	Allow []egress.DeclaredAllowance
	// Deny lists the declared refusals.
	Deny []egress.DeclaredDenial
	// Mediated reports whether the engine's own forward proxy will apply the
	// declaration rather than the driver.
	Mediated bool
	// Enforcement is the mechanism that will apply the declaration.
	Enforcement SandboxNetworkEnforcement
	// Enforced reports whether that mechanism applies the declaration.
	Enforced bool
	// Mechanisms lists every enforcement mechanism that will apply at least part
	// of this declaration, in driver-first order: for example
	// ["sdk_network_policy", "forward_proxy"]. Mechanism above remains the one
	// that determines the reported strength, so a consumer that only knows the
	// primary mechanism keeps its existing interpretation.
	Mechanisms []string
	// Limitations states material limits of the reported enforcement that do not
	// make it unenforced, such as traffic that bypasses the forward proxy. It is
	// empty when there is nothing beyond Mechanism to disclose.
	Limitations []string
	// DaemonPolicySource and DaemonPolicySHA256 identify the daemon-wide policy
	// the effective declaration was computed against. They are empty when no
	// daemon-wide policy was configured, or when this report was produced
	// outside project admission.
	DaemonPolicySource string
	DaemonPolicySHA256 string
	// Unapplied names the parts of the declaration the mechanism does not apply.
	// It is empty when the mechanism applies all of it.
	Unapplied []string
	// Warning states what the operator must know when the declaration will not be
	// enforced as written. It is empty when it will be.
	Warning string
}

// mechanismSeccompRedirect names the sandbox-init mediation that makes the
// forward proxy authoritative even for process that ignores the proxy
// environment.
const mechanismSeccompRedirect = "seccomp_redirect"

// mechanismKubernetesEgressPolicy is the per-sandbox CNI default-deny egress
// NetworkPolicy. It is a real outer boundary for every process in the Pod, not
// only for processes started through ac-init.
const mechanismKubernetesEgressPolicy = "kubernetes_network_policy"

// mechanismDockerInternalNetwork is the strict Docker sandbox user-defined
// internal network. It is the network-namespace outer boundary for every
// process in the container, not only for processes started through ac-init.
const mechanismDockerInternalNetwork = "docker_internal_network"

// ReportAgentNetworkPolicy accounts for one agent's declared policy on one
// resolved driver. mediated reports whether the engine's forward proxy will be
// in this sandbox's traffic path; options.InitMediated reports whether the sandbox init
// process redirects connects to that proxy. mode is the deployment's
// EGRESS_UNENFORCEABLE_POLICY, which decides what an unenforceable declaration
// means so the report can state the outcome instead of restating the knob.
//
// A nil declaration reports Declared false: the engine cannot restrict a sandbox
// that did not ask, so there is no mechanism to name and no gap to report.
// NetworkPolicyReportOptions is the deployment-dependent half of a network
// policy report.
type NetworkPolicyReportOptions struct {
	// Mediated reports whether the engine's forward proxy is in the traffic
	// path.
	Mediated bool
	// InitMediated reports whether the sandbox init process redirects connects
	// to that proxy.
	InitMediated bool
	// K8sEgressNetworkPolicy reports whether the k8s driver installs a
	// per-sandbox default-deny egress NetworkPolicy. It is only effective when
	// the cluster's CNI enforces NetworkPolicy.
	K8sEgressNetworkPolicy bool
	// DockerInternalNetwork reports whether the strict Docker sandbox network
	// profile is selected, so a sandbox joins only a user-defined internal
	// network and every process in its network namespace is denied off-network
	// egress.
	DockerInternalNetwork bool
	// Mode is the deployment's EGRESS_UNENFORCEABLE_POLICY.
	Mode config.EgressUnenforceablePolicy
}

func ReportAgentNetworkPolicy(agent, driver string, declaration *egress.NetworkDeclaration, options NetworkPolicyReportOptions) (AgentNetworkPolicyReport, error) {
	mediated := options.Mediated
	initMediated := options.InitMediated
	mode := options.Mode
	resolved := resolveRuntimeDriver(driver)
	report := AgentNetworkPolicyReport{Agent: agent, Driver: resolved}
	if declaration == nil {
		return report, nil
	}
	// egress.ReportNetworkPolicy compiles the declaration first, so a report that
	// disagrees with the decision model is impossible.
	declared, err := egress.ReportNetworkPolicy(declaration, nil)
	if err != nil {
		return AgentNetworkPolicyReport{}, fmt.Errorf("report declared network policy for agent %q: %w", agent, err)
	}
	report.Declared = true
	report.Default = declared.Default
	report.Allow = declared.Allow
	report.Deny = declared.Deny
	if !declared.Restricts {
		// Documented and deliberate: an allow list with an allow default cannot
		// refuse anything, so there is no mechanism to report.
		return report, nil
	}
	report.Restricts = true

	driverEnforcement := SandboxNetworkEnforcementFor(resolved)
	if driverEnforcement.Strength != SandboxEgressStrengthNone {
		report.Mechanisms = append(report.Mechanisms, driverEnforcement.Mechanism)
	}
	if mediated {
		report.Mechanisms = append(report.Mechanisms, mechanismForwardProxy)
	}
	if initMediated {
		report.Mechanisms = append(report.Mechanisms, mechanismSeccompRedirect)
	}
	if resolved == RuntimeDriverK8s && options.K8sEgressNetworkPolicy {
		report.Mechanisms = append(report.Mechanisms, mechanismKubernetesEgressPolicy)
		report.Limitations = append(report.Limitations,
			"the per-sandbox NetworkPolicy admits only the engine-owned endpoints; a cluster whose CNI does not enforce NetworkPolicy does not provide this layer")
	}
	if resolved == RuntimeDriverDocker && options.DockerInternalNetwork {
		report.Mechanisms = append(report.Mechanisms, mechanismDockerInternalNetwork)
		report.Limitations = append(report.Limitations,
			"the strict Docker internal network admits only the daemon endpoints on that network, the host bridge gateway, and other containers attached to it; every off-network destination is denied for every process in the container, including one started outside sandbox init, although Docker's embedded DNS resolver still answers queries")
	}
	enforcement := driverEnforcement
	if mediated {
		enforcement = SandboxEgressForwardProxyEnforcement(resolved)
		// The proxy can only attribute a binary when the sandbox init process
		// supplies it. Without that mediation a connection carries no
		// executable, which no binary-scoped allowance can satisfy, so the
		// mechanism does not apply the binary scope even though it decides the
		// destination.
		if !initMediated {
			enforcement.AppliesBinaries = false
		}
		report.Mediated = true
	}
	if declaration.Default != egress.Deny && len(declaration.Deny) > 0 && enforcement.AppliesDenyEntries {
		enforcement.Strength = SandboxEgressStrengthDenyList
	}
	report.Enforcement = enforcement

	// The engine never claims a limit it does not install: the parts this
	// mechanism cannot apply are named, including the address constraint the
	// creation path also refuses to present as applied. A declared deny domain is
	// not in this list because the declaration cannot carry one yet; the creation
	// path reads them from the driver-boundary policy, so they have to reach this
	// function through egress.NetworkDeclaration in the same change that makes
	// them declarable (see the file comment).
	addresses := egress.AddressConstrainedAllowances(declaration.Allow)
	binaries := egress.BinaryScopedAllowances(declaration.Allow)
	binaryDenials := egress.BinaryScopedDenials(declaration.Deny)
	auditAllowances := 0
	for _, entry := range declaration.Allow {
		if entry.Enforcement == egress.EnforcementAudit {
			auditAllowances++
		}
	}
	addressGapOnBypass := mediated && addresses > 0 &&
		driverEnforcement.Strength != SandboxEgressStrengthNone && !driverEnforcement.AppliesAllowedAddresses
	switch {
	case len(declaration.Allow) > 0 && !enforcement.AppliesAllowEntries:
		report.Unapplied = append(report.Unapplied, "the declared allowances")
	case addresses > 0 && !enforcement.AppliesAllowedAddresses:
		report.Unapplied = append(report.Unapplied, "the addresses the declared allowances may resolve to")
	}
	if addressGapOnBypass {
		report.Unapplied = append(report.Unapplied, "the addresses the declared allowances may resolve to on traffic that bypasses the forward proxy")
	}
	if binaries > 0 && !enforcement.AppliesBinaries {
		report.Unapplied = append(report.Unapplied, "the binaries the declared allowances are scoped to")
	}
	if binaryDenials > 0 && !enforcement.AppliesBinaries {
		report.Unapplied = append(report.Unapplied, "the binaries the declared denials are scoped to")
	}
	if len(declaration.Deny) > 0 && !enforcement.AppliesDenyEntries {
		report.Unapplied = append(report.Unapplied, "the declared denials")
	}
	if !enforcement.AppliesEngineEndpoints {
		report.Unapplied = append(report.Unapplied, "the engine's own sandbox endpoints")
	}
	if mediated {
		if initMediated {
			report.Limitations = append(report.Limitations,
				"the sandbox init process redirects every TCP connect through the forward proxy, so a process that ignores the proxy environment is still decided; non-DNS UDP is refused and DNS is answered by a sandbox-local policy resolver, but a workload that uses sendmmsg can still send UDP directly, because that syscall stays unfiltered so the init trampoline can hand over its notification listener")
			if binaries > 0 || binaryDenials > 0 {
				report.Limitations = append(report.Limitations,
					"the forward proxy attributes a connection to the executable the sandbox init process reads from /proc/<pid>/exe; a connection that does not cross that process carries no binary attribution, which no binary-scoped allowance can satisfy and every binary-scoped denial still refuses")
			}
		} else {
			report.Limitations = append(report.Limitations,
				"the engine's forward proxy decides only traffic that honors the proxy environment; a process that bypasses it is not decided by the proxy")
			if binaries > 0 || binaryDenials > 0 {
				report.Limitations = append(report.Limitations,
					"the forward proxy has no executable attribution without the sandbox init redirect, so a binary-scoped allowance cannot match and a binary-scoped denial refuses every executable")
			}
		}
		if addresses > 0 && driverEnforcement.Strength != SandboxEgressStrengthNone && !driverEnforcement.AppliesAllowedAddresses {
			report.Limitations = append(report.Limitations, fmt.Sprintf(
				"%s applies the declared destinations but not allowed_ips; traffic that bypasses the forward proxy is not address-constrained",
				driverEnforcement.Mechanism))
		}
	}
	if auditAllowances > 0 {
		if mediated {
			report.Limitations = append(report.Limitations, fmt.Sprintf(
				"%d allowance(s) declare enforcement: audit; the forward proxy records the match and allows the request instead of enforcing it, and a driver mechanism that also applies the declaration has no per-entry audit surface",
				auditAllowances))
		} else {
			report.Limitations = append(report.Limitations, fmt.Sprintf(
				"%d allowance(s) declare enforcement: audit, but %s has no per-entry audit surface and applies them as enforcement",
				auditAllowances, enforcement.Mechanism))
		}
	}

	if enforcement.Strength == SandboxEgressStrengthNone {
		report.Warning = fmt.Sprintf(
			"the engine will not enforce this declaration on driver %q: %s; %s",
			resolved, enforcement.Notes, unenforceableOutcome(mode),
		)
		return report, nil
	}
	binaryGap := (binaries > 0 || binaryDenials > 0) && !enforcement.AppliesBinaries
	refuses := (addresses > 0 && !enforcement.AppliesAllowedAddresses) || addressGapOnBypass || binaryGap ||
		(len(declaration.Deny) > 0 && !enforcement.AppliesDenyEntries)
	if refuses && mode == config.EgressUnenforceableRefuse {
		// The creation path refuses exactly these gaps, so a report that
		// answered Enforced would describe a sandbox that will never start.
		report.Warning = fmt.Sprintf("%s does not apply %s; %s",
			enforcement.Mechanism, strings.Join(report.Unapplied, ", "), unenforceableOutcome(mode))
		return report, nil
	}
	report.Enforced = true
	if len(report.Unapplied) > 0 {
		report.Warning = fmt.Sprintf("%s applies the declaration, but not %s; %s",
			enforcement.Mechanism, strings.Join(report.Unapplied, ", "), enforcement.Notes)
	}
	return report, nil
}

// unenforceableOutcome states what the deployment's policy does with an
// unenforceable declaration, so the report answers "and then what" rather than
// leaving the operator to look up the knob.
func unenforceableOutcome(mode config.EgressUnenforceablePolicy) string {
	if mode == config.EgressUnenforceableRefuse {
		return "a sandbox that declares it is refused (" + config.EgressUnenforceablePolicyName + "=refuse)"
	}
	return "a sandbox that declares it starts with unrestricted egress (" + config.EgressUnenforceablePolicyName + "=" + string(config.EgressUnenforceableWarn) + ")"
}
