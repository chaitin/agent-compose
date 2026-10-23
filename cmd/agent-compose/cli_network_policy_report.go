package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/egress"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// composeNetworkPolicyOutput is the per-agent admission account of a declared
// outbound network policy: what was declared, and the mechanism that will apply
// it. It is rendered from the daemon's report rather than re-derived here,
// because only the daemon knows which drivers it compiled and whether its
// forward proxy can reach a sandbox.
type composeNetworkPolicyOutput struct {
	Agent     string                      `json:"agent"`
	Driver    string                      `json:"driver"`
	Restricts bool                        `json:"restricts"`
	Default   string                      `json:"default"`
	Allow     []composeNetworkAllowOutput `json:"allow"`
	Deny      []composeNetworkDenyOutput  `json:"deny"`
	Mediated  bool                        `json:"mediated"`
	Enforced  bool                        `json:"enforced"`
	Strength  string                      `json:"strength"`
	Mechanism string                      `json:"mechanism"`
	// Mechanisms lists every mechanism that applies part of the declaration.
	Mechanisms []string `json:"mechanisms,omitempty"`
	// Limitations states material limits that do not make the declaration
	// unenforced, such as traffic bypassing the forward proxy.
	Limitations []string `json:"limitations,omitempty"`
	// DaemonPolicySource and DaemonPolicySHA256 identify the daemon-wide policy
	// the effective declaration was computed against.
	DaemonPolicySource string   `json:"daemon_policy_source,omitempty"`
	DaemonPolicySHA256 string   `json:"daemon_policy_sha256,omitempty"`
	Unapplied          []string `json:"unapplied,omitempty"`
	Warning            string   `json:"warning,omitempty"`
}

type composeNetworkAllowOutput struct {
	Endpoint   string   `json:"endpoint"`
	AllowedIPs []string `json:"allowed_ips,omitempty"`
	Sources    []string `json:"sources,omitempty"`
}

type composeNetworkDenyOutput struct {
	Endpoint string   `json:"endpoint"`
	Sources  []string `json:"sources,omitempty"`
}

func composeNetworkPoliciesFromProto(reports []*agentcomposev2.AgentNetworkPolicyReport) []composeNetworkPolicyOutput {
	if len(reports) == 0 {
		return nil
	}
	policies := make([]composeNetworkPolicyOutput, 0, len(reports))
	for _, report := range reports {
		allow := make([]composeNetworkAllowOutput, 0, len(report.GetAllow()))
		for _, entry := range report.GetAllow() {
			allow = append(allow, composeNetworkAllowOutput{
				Endpoint:   entry.GetEndpoint(),
				AllowedIPs: append([]string(nil), entry.GetAllowedIps()...),
				Sources:    append([]string(nil), entry.GetSources()...),
			})
		}
		deny := make([]composeNetworkDenyOutput, 0, len(report.GetDeny()))
		for _, entry := range report.GetDeny() {
			deny = append(deny, composeNetworkDenyOutput{
				Endpoint: entry.GetEndpoint(),
				Sources:  append([]string(nil), entry.GetSources()...),
			})
		}
		policies = append(policies, composeNetworkPolicyOutput{
			Agent:              report.GetAgent(),
			Driver:             report.GetDriver(),
			Restricts:          report.GetRestricts(),
			Default:            report.GetDefault(),
			Allow:              allow,
			Deny:               deny,
			Mediated:           report.GetMediated(),
			Enforced:           report.GetEnforced(),
			Strength:           report.GetStrength(),
			Mechanism:          report.GetMechanism(),
			Mechanisms:         append([]string(nil), report.GetMechanisms()...),
			Limitations:        append([]string(nil), report.GetLimitations()...),
			DaemonPolicySource: report.GetDaemonPolicySource(),
			DaemonPolicySHA256: report.GetDaemonPolicySha256(),
			Unapplied:          append([]string(nil), report.GetUnapplied()...),
			Warning:            report.GetWarning(),
		})
	}
	return policies
}

// composeNetworkPolicyWarnings collects the declarations the engine will not
// enforce as written, so they are visible on stderr even when the report itself
// is redirected. Agents that share one gap share one warning: the reason is a
// property of the mechanism, and repeating it per agent would bury the agents it
// applies to.
func composeNetworkPolicyWarnings(policies []composeNetworkPolicyOutput) []string {
	byWarning := make(map[string][]string)
	order := make([]string, 0, len(policies))
	for _, policy := range policies {
		warning := strings.TrimSpace(policy.Warning)
		if warning == "" {
			continue
		}
		if _, seen := byWarning[warning]; !seen {
			order = append(order, warning)
		}
		byWarning[warning] = append(byWarning[warning], policy.Agent)
	}
	warnings := make([]string, 0, len(order))
	for _, warning := range order {
		agents := byWarning[warning]
		sort.Strings(agents)
		noun := "agent"
		if len(agents) > 1 {
			noun = "agents"
		}
		warnings = append(warnings, fmt.Sprintf("%s %s: %s", noun, strings.Join(agents, ", "), warning))
	}
	return warnings
}

// writeComposeNetworkPolicyReport renders what each agent declared and what the
// engine will do about it. The lines are ordered so the declared fact and the
// enforced mechanism are adjacent: the point of the report is the difference
// between them.
func writeComposeNetworkPolicyReport(out io.Writer, policies []composeNetworkPolicyOutput) error {
	if len(policies) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, "Network policy"); err != nil {
		return err
	}
	if err := writeDaemonNetworkPolicyIdentity(out, policies); err != nil {
		return err
	}
	for _, policy := range policies {
		if err := writeComposeAgentNetworkPolicy(out, policy); err != nil {
			return err
		}
	}
	return nil
}

func writeComposeAgentNetworkPolicy(out io.Writer, policy composeNetworkPolicyOutput) error {
	if _, err := fmt.Fprintf(out, "  %s (driver %s)\n", policy.Agent, policy.Driver); err != nil {
		return err
	}
	defaultLabel := networkPolicyDefaultLabel(policy.Default)
	if !policy.Restricts {
		// An undeclared policy cannot reach here, so this is a declared policy
		// that refuses nothing: the documented behavior of an allow list without
		// a deny default.
		if _, err := fmt.Fprintf(out, "    declared  default %s with %d allowance(s): egress is not restricted, because an allow list needs default: deny-all and no denial matches\n", defaultLabel, len(policy.Allow)); err != nil {
			return err
		}
		return writeComposeNetworkAllowances(out, policy.Allow)
	}
	if _, err := fmt.Fprintf(out, "    declared  default %s, %d allowance(s), %d denial(s)\n", defaultLabel, len(policy.Allow), len(policy.Deny)); err != nil {
		return err
	}
	for _, entry := range policy.Deny {
		if _, err := fmt.Fprintf(out, "      deny  %s%s\n", entry.Endpoint, networkPolicySourcesSuffix(entry.Sources)); err != nil {
			return err
		}
	}
	if err := writeComposeNetworkAllowances(out, policy.Allow); err != nil {
		return err
	}
	if !policy.Enforced {
		if _, err := fmt.Fprintf(out, "    enforced  no: %s\n", policy.Warning); err != nil {
			return err
		}
	} else {
		mechanism := fmt.Sprintf("%s (%s)", policy.Mechanism, policy.Strength)
		switch {
		case len(policy.Mechanisms) > 1:
			mechanism += fmt.Sprintf("; mechanisms: %s", strings.Join(policy.Mechanisms, ", "))
		case policy.Mediated:
			mechanism += ", the engine's forward proxy"
		}
		if _, err := fmt.Fprintf(out, "    enforced  %s\n", mechanism); err != nil {
			return err
		}
	}
	// The parts the mechanism leaves are stated whenever they exist, enforced or
	// not: a partially applied declaration is the case a reader is most likely to
	// mistake for a complete one.
	if len(policy.Unapplied) > 0 {
		if _, err := fmt.Fprintf(out, "    partial   %s\n", strings.Join(policy.Unapplied, ", ")); err != nil {
			return err
		}
	}
	for _, limitation := range policy.Limitations {
		if _, err := fmt.Fprintf(out, "    limit     %s\n", limitation); err != nil {
			return err
		}
	}
	return nil
}

// writeDaemonNetworkPolicyIdentity states the daemon policy file and digest
// once, before the per-agent accounts. Repeating it on every agent would make
// the report noisy while still leaving the first occurrence easy to miss.
func writeDaemonNetworkPolicyIdentity(out io.Writer, policies []composeNetworkPolicyOutput) error {
	for _, policy := range policies {
		if policy.DaemonPolicySource == "" {
			continue
		}
		identity := "  daemon policy  " + policy.DaemonPolicySource
		if policy.DaemonPolicySHA256 != "" {
			identity += " (sha256:" + policy.DaemonPolicySHA256 + ")"
		}
		_, err := fmt.Fprintln(out, identity)
		return err
	}
	return nil
}

func networkPolicySourcesSuffix(sources []string) string {
	if len(sources) == 0 {
		return ""
	}
	return " [" + strings.Join(sources, "+") + "]"
}

func writeComposeNetworkAllowances(out io.Writer, allow []composeNetworkAllowOutput) error {
	for _, entry := range allow {
		if len(entry.AllowedIPs) > 0 {
			if _, err := fmt.Fprintf(out, "      allow %s (addresses %s)%s\n", entry.Endpoint, strings.Join(entry.AllowedIPs, " "), networkPolicySourcesSuffix(entry.Sources)); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(out, "      allow %s%s\n", entry.Endpoint, networkPolicySourcesSuffix(entry.Sources)); err != nil {
			return err
		}
	}
	return nil
}

// writeComposeDeclaredNetworkPolicyNotes reports what a project declares about
// sandbox egress without claiming what the engine will do about it. `config`
// reads the project locally and never contacts the daemon, so the enforcement
// mechanism — which depends on the daemon's compiled drivers and whether its
// forward proxy can reach a sandbox — is decided and reported by `up`/`apply`.
//
// It is written to stderr so the canonical spec on stdout keeps its contract,
// and it stays silent for a project that declares no policy.
func writeComposeDeclaredNetworkPolicyNotes(out io.Writer, spec *compose.NormalizedProjectSpec) error {
	if spec == nil {
		return nil
	}
	names := make([]string, 0, len(spec.Agents))
	networks := make(map[string]*compose.NormalizedSandboxNetworkSpec, len(spec.Agents))
	for _, agent := range spec.Agents {
		// PolicyBlock is nil for an agent that declared no policy block at all,
		// which is the common case and must stay a no-op.
		policy := agent.PolicyBlock()
		if policy == nil || policy.Network == nil {
			continue
		}
		names = append(names, agent.Name)
		networks[agent.Name] = policy.Network
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	if _, err := fmt.Fprintln(out, "declared network policy (what the daemon will enforce is reported by up/apply):"); err != nil {
		return err
	}
	for _, name := range names {
		declaration, err := networks[name].EgressDeclaration()
		if err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
		// The declared half of the account comes from the same function the
		// daemon's report uses, so the two cannot describe different policies.
		report, err := egress.ReportNetworkPolicy(&declaration, nil)
		if err != nil {
			return fmt.Errorf("agent %s: %w", name, err)
		}
		if err := writeDeclaredNetworkPolicy(out, name, report); err != nil {
			return err
		}
	}
	return nil
}

// networkPolicyDefaultLabel renders a policy action using the compose mode
// vocabulary a reader sees in the YAML. The internal action remains allow or
// deny; the "-all" suffix is what distinguishes the mode from an allow or deny
// entry in the same block.
func networkPolicyDefaultLabel(action string) string {
	switch action {
	case string(egress.Deny):
		return "deny-all"
	case string(egress.Allow):
		return "allow-all"
	default:
		return action
	}
}

func writeDeclaredNetworkPolicy(out io.Writer, agent string, report egress.NetworkPolicyReport) error {
	if _, err := fmt.Fprintf(out, "  %s  default %s, %d allowance(s), %d denial(s)\n", agent, networkPolicyDefaultLabel(string(report.Default)), len(report.Allow), len(report.Deny)); err != nil {
		return err
	}
	if !report.Restricts && len(report.Allow) > 0 {
		if _, err := fmt.Fprintln(out, "    egress is not restricted: an allow list alone does not restrict; add default: deny-all or a deny entry"); err != nil {
			return err
		}
	}
	for _, entry := range report.Allow {
		if len(entry.AllowedIPs) > 0 {
			if _, err := fmt.Fprintf(out, "    allow %s (addresses %s)\n", entry.Endpoint, strings.Join(entry.AllowedIPs, " ")); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(out, "    allow %s\n", entry.Endpoint); err != nil {
			return err
		}
	}
	for _, entry := range report.Deny {
		if _, err := fmt.Fprintf(out, "    deny  %s\n", entry.Endpoint); err != nil {
			return err
		}
	}
	return nil
}
