package api

import (
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// NetworkPolicyReportsToProto renders the daemon's account of what each agent
// declared and which mechanism will apply it. It is transport-only: the report
// was computed from the same declaration the decision model compiles, so it
// never re-derives or reinterprets the policy here.
func NetworkPolicyReportsToProto(reports []driverpkg.AgentNetworkPolicyReport) []*agentcomposev2.AgentNetworkPolicyReport {
	if len(reports) == 0 {
		return nil
	}
	mapped := make([]*agentcomposev2.AgentNetworkPolicyReport, 0, len(reports))
	for _, report := range reports {
		allow := make([]*agentcomposev2.DeclaredNetworkAllowance, 0, len(report.Allow))
		for _, entry := range report.Allow {
			allow = append(allow, &agentcomposev2.DeclaredNetworkAllowance{
				Endpoint:   entry.Endpoint,
				AllowedIps: append([]string(nil), entry.AllowedIPs...),
				Sources:    append([]string(nil), entry.Sources...),
			})
		}
		deny := make([]*agentcomposev2.DeclaredNetworkDenial, 0, len(report.Deny))
		for _, entry := range report.Deny {
			deny = append(deny, &agentcomposev2.DeclaredNetworkDenial{
				Endpoint: entry.Endpoint,
				Sources:  append([]string(nil), entry.Sources...),
			})
		}
		mapped = append(mapped, &agentcomposev2.AgentNetworkPolicyReport{
			Agent:              report.Agent,
			Driver:             report.Driver,
			Declared:           report.Declared,
			Restricts:          report.Restricts,
			Default:            networkPolicyDefaultMode(report.Default),
			Allow:              allow,
			Deny:               deny,
			Mediated:           report.Mediated,
			Enforced:           report.Enforced,
			Strength:           string(report.Enforcement.Strength),
			Mechanism:          report.Enforcement.Mechanism,
			Mechanisms:         append([]string(nil), report.Mechanisms...),
			Limitations:        append([]string(nil), report.Limitations...),
			DaemonPolicySource: report.DaemonPolicySource,
			DaemonPolicySha256: report.DaemonPolicySHA256,
			Unapplied:          append([]string(nil), report.Unapplied...),
			Warning:            report.Warning,
		})
	}
	return mapped
}

// networkPolicyDefaultMode renders the internal allow/deny action with the
// compose mode vocabulary a consumer sees in the declaration. The "-all"
// suffix keeps the mode distinct from an allow or deny entry.
func networkPolicyDefaultMode(action egress.Action) string {
	switch action {
	case egress.Deny:
		return "deny-all"
	case egress.Allow:
		return "allow-all"
	default:
		return string(action)
	}
}
