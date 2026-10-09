package driver

import (
	"fmt"
	"strconv"
	"strings"
)

// This file owns the pure mapping from the driver-boundary SandboxNetworkPolicy
// into the Microsandbox network model.
//
// Why the mapping targets a small local struct instead of
// *microsandbox.NetworkConfig directly: the Microsandbox Go SDK is imported only
// under the `linux && cgo && microsandboxcgo` build tag (see
// microsandbox_runtime.go), and an untagged mapping here must not pull the SDK
// into every build, including the docker-only macOS binary. The build-tagged
// adapter that turns a plan into *microsandbox.NetworkConfig lives beside the
// runtime that imports the SDK.
//
// SDK uncertainty: go.mod requires
// github.com/superradcompany/microsandbox/sdk/go v0.7.3. The mapping was written
// against that version's options.go, where NetworkConfig carries
// Rules []PolicyRule, DefaultEgress, DefaultIngress, DenyDomains, DenyDomainSuffixes,
// DNS *DNSConfig, and DNSRebindProtection, and PolicyRule carries
// Action/Direction/Destination/Protocol(s)/Port(s). A v0.7.3 SDK is available in
// the module cache here, so the field names are confirmed, but the sandbox
// hypervisor semantics of a domain-suffix destination (".example.com") versus a
// label wildcard ("*.example.com") are not verified without a real KVM run. The
// plan therefore converts a single leading wildcard label to the SDK's suffix
// form and refuses any other pattern rather than guessing.

// microsandboxNetworkPlan is the SDK-independent rendering of a sandbox network
// policy for the Microsandbox runtime.
type microsandboxNetworkPlan struct {
	// DefaultEgress is the SDK PolicyAction value for unmatched egress.
	DefaultEgress string
	// Rules are the ordered allow rules an enforcing driver applies.
	Rules []microsandboxNetworkRule
	// DenyDomains are exact domains the in-VM DNS proxy refuses to resolve.
	DenyDomains []string
	// RebindProtection keeps the in-VM DNS rebind protection enabled. It is
	// always true: the engine previously disabled it to let guests resolve
	// names that point at private addresses, which is exactly the rebinding
	// behavior the protection exists to stop.
	RebindProtection bool
}

// microsandboxNetworkRule is one firewall rule in a plan.
type microsandboxNetworkRule struct {
	Action      string
	Destination string
	Port        string
	Protocol    string
}

// microsandboxPlanDefaultEgressAllow and microsandboxPlanDefaultEgressDeny
// mirror the SDK's PolicyAction values. They are duplicated strings rather than
// SDK constants because this file must not import the SDK.
const (
	microsandboxPlanDefaultEgressAllow = "allow"
	microsandboxPlanDefaultEgressDeny  = "deny"
)

// planMicrosandboxNetwork maps a policy onto the Microsandbox network model. A
// nil policy renders the undeclared behavior: egress allowed, no rules, rebind
// protection on. A declared allow-all policy renders the same, because its
// allow entries are no-ops, while a declared deny policy renders a deny default
// with the engine-owned endpoints first and the declared allowances after them.
//
// The function is pure and returns an error instead of dropping a host pattern
// it cannot express, so a declaration the mapping cannot honor fails closed.
func planMicrosandboxNetwork(policy *SandboxNetworkPolicy) (microsandboxNetworkPlan, error) {
	plan := microsandboxNetworkPlan{
		DefaultEgress:    microsandboxPlanDefaultEgressAllow,
		RebindProtection: true,
	}
	if policy == nil {
		return plan, nil
	}
	if err := policy.Validate(); err != nil {
		return microsandboxNetworkPlan{}, err
	}
	plan.DenyDomains = append([]string(nil), policy.DenyDomains...)
	if !policy.DenyByDefault() {
		return plan, nil
	}
	plan.DefaultEgress = microsandboxPlanDefaultEgressDeny
	for _, entry := range policy.permittingEntries() {
		destination, err := microsandboxDestination(entry.Host)
		if err != nil {
			return microsandboxNetworkPlan{}, err
		}
		plan.Rules = append(plan.Rules, microsandboxNetworkRule{
			Action:      "allow",
			Destination: destination,
			Port:        strconv.Itoa(entry.Port),
			Protocol:    microsandboxProtocol(entry.normalizedProtocol()),
		})
	}
	return plan, nil
}

// microsandboxDestination renders one entry host as a PolicyRule destination.
// An exact host is passed through. A single leading wildcard label becomes the
// SDK's domain-suffix form (".example.com"); every other pattern is rejected
// because the SDK does not document a general pattern destination.
func microsandboxDestination(host string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if !strings.Contains(normalized, "*") {
		return normalized, nil
	}
	if strings.HasPrefix(normalized, "*.") && !strings.Contains(normalized[2:], "*") {
		return "." + normalized[2:], nil
	}
	return "", fmt.Errorf("microsandbox network mapping cannot express host pattern %q", host)
}

// microsandboxProtocol renders the L4 protocol the SDK firewall can match. The
// SDK set is tcp/udp/icmpv4/icmpv6 with an empty value meaning any; the L7
// http/https intent is enforced as TCP because a firewall rule cannot itself
// inspect a request. Attempting to map http to anything narrower would claim
// enforcement the rule does not provide.
func microsandboxProtocol(protocol SandboxNetworkProtocol) string {
	switch protocol {
	case SandboxNetworkProtocolTCP, SandboxNetworkProtocolHTTP, SandboxNetworkProtocolHTTPS:
		return "tcp"
	case SandboxNetworkProtocolUDP:
		return "udp"
	default:
		return ""
	}
}
