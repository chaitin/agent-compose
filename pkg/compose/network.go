package compose

import (
	"fmt"
	"slices"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// Sandbox network policy modes. DefaultAllowAll is the value an omitted
// default normalizes to; it is not an engine default, because the whole block
// has to be declared before any of this applies (D3). The "-all" suffix keeps
// the mode distinct from the allow and deny entries inside the block.
const (
	SandboxNetworkDefaultAllowAll = "allow-all"
	SandboxNetworkDefaultDenyAll  = "deny-all"
)

// NormalizeSandboxNetworkSpec validates a declared sandbox network policy and
// returns its normalized form, or nil when the block was not declared. The
// default selects the mode: deny-all is allowlist mode and allow-all is
// denylist mode; a deny entry wins over an allow entry in either mode.
//
// The normalized types stay in pkg/compose so the compose schema owns its own
// declaration; converting to the decision model is a separate, explicit step
// (EgressDeclaration), which is what keeps a single policy representation.
func NormalizeSandboxNetworkSpec(path string, network *SandboxNetworkSpec) (*NormalizedSandboxNetworkSpec, error) {
	if network == nil {
		return nil, nil
	}
	defaultAction, err := normalizeSandboxNetworkDefault(network.Default)
	if err != nil {
		return nil, &ValidationError{Path: path + ".default", Message: err.Error()}
	}
	allow, err := normalizeSandboxNetworkAllow(path+".allow", network.Allow)
	if err != nil {
		return nil, err
	}
	deny, err := normalizeSandboxNetworkDeny(path+".deny", network.Deny)
	if err != nil {
		return nil, err
	}
	return &NormalizedSandboxNetworkSpec{Default: defaultAction, Allow: allow, Deny: deny}, nil
}

// EgressDeclaration expresses the normalized declaration in the single egress
// policy model. It is pure.
//
// The schema and the decision model name the permissive default differently:
// the schema keeps "allow-all" so a declared policy stays visibly distinct from
// the engine's own default (D3), while the decision model only knows allow and
// deny. This method is the one place that translates between the two
// vocabularies, so no caller has to know either one.
//
// The normalized spec a caller decodes from persisted canonical JSON has not
// been re-validated against the schema, so the default is normalized again with
// the schema's own rule and a value the schema does not accept is an error
// instead of a guessed action: guessing could silently widen egress.
func (n *NormalizedSandboxNetworkSpec) EgressDeclaration() (egress.NetworkDeclaration, error) {
	if n == nil {
		return egress.NetworkDeclaration{}, nil
	}
	defaultAction, err := egressActionForSandboxNetworkDefault(n.Default)
	if err != nil {
		return egress.NetworkDeclaration{}, err
	}
	declaration := egress.NetworkDeclaration{Default: defaultAction}
	for _, entry := range n.Allow {
		declaration.Allow = append(declaration.Allow, egress.AllowEntry{
			Host:       entry.Host,
			Port:       entry.Port,
			Protocol:   egress.ProtocolAny,
			AllowedIPs: entry.AllowedIPs,
		})
	}
	for _, entry := range n.Deny {
		declaration.Deny = append(declaration.Deny, egress.DenyEntry{
			Host:     entry.Host,
			Port:     entry.Port,
			Protocol: egress.ProtocolAny,
		})
	}
	return declaration, nil
}

// egressActionForSandboxNetworkDefault translates the schema's default value
// into the decision model's action. It reuses the schema normalizer, so the
// omitted default and the accepted spellings are defined in exactly one place;
// a value the schema does not accept is an error rather than a guessed action.
func egressActionForSandboxNetworkDefault(value string) (egress.Action, error) {
	normalized, err := normalizeSandboxNetworkDefault(value)
	if err != nil {
		return "", err
	}
	if normalized == SandboxNetworkDefaultDenyAll {
		return egress.Deny, nil
	}
	return egress.Allow, nil
}

func normalizeSandboxNetworkDefault(value string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "":
		return SandboxNetworkDefaultAllowAll, nil
	case SandboxNetworkDefaultAllowAll:
		return normalized, nil
	case SandboxNetworkDefaultDenyAll:
		return SandboxNetworkDefaultDenyAll, nil
	case "deny":
		// The old spelling is kept as a migration diagnostic rather than falling
		// through to the generic message: it is a real, previously accepted value
		// and an operator upgrading an existing file needs the replacement, not a
		// list of accepted values to decode.
		return "", fmt.Errorf("network default %q is no longer supported; use %q", value, SandboxNetworkDefaultDenyAll)
	default:
		return "", fmt.Errorf("network default must be %q or %q", SandboxNetworkDefaultAllowAll, SandboxNetworkDefaultDenyAll)
	}
}

// Bounds on one declared allow or deny list and its port expansion. Every entry
// and every allowed address is validated, deduplicated, and compiled before the
// declaration can be refused, so an unbounded list is work a request makes the
// daemon do. The limits are far above any real declaration and exist only to
// bound that work; a ports list is capped by the same entry limit.
const (
	maxSandboxNetworkAllowEntries    = 256
	maxSandboxNetworkAllowAllowedIPs = 256
	maxSandboxNetworkDenyEntries     = 256
)

func normalizeSandboxNetworkDeny(path string, values []SandboxNetworkDenySpec) ([]NormalizedSandboxNetworkDenySpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxSandboxNetworkDenyEntries {
		return nil, &ValidationError{Path: path, Message: fmt.Sprintf("at most %d deny entries are supported, got %d", maxSandboxNetworkDenyEntries, len(values))}
	}
	normalized := make([]NormalizedSandboxNetworkDenySpec, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		host, err := egress.NormalizeHost(value.Host)
		if err != nil {
			return nil, &ValidationError{Path: entryPath + ".host", Message: err.Error()}
		}
		ports, err := normalizeSandboxNetworkPorts(entryPath, value.Port, value.Ports, maxSandboxNetworkDenyEntries)
		if err != nil {
			return nil, err
		}
		for _, port := range ports {
			dedupeKey := fmt.Sprintf("%s\x00%d", host, port)
			if _, duplicate := seen[dedupeKey]; duplicate {
				return nil, &ValidationError{Path: entryPath, Message: fmt.Sprintf("duplicate deny entry for %q port %d", host, port)}
			}
			seen[dedupeKey] = struct{}{}
			normalized = append(normalized, NormalizedSandboxNetworkDenySpec{
				Host: host,
				Port: port,
			})
		}
		if len(normalized) > maxSandboxNetworkDenyEntries {
			return nil, &ValidationError{Path: path, Message: fmt.Sprintf("at most %d deny entries are supported after expanding ports", maxSandboxNetworkDenyEntries)}
		}
	}
	// Deny entries are a set like allowances: declaration order carries no
	// meaning because any matching denial refuses the destination.
	slices.SortFunc(normalized, func(a, b NormalizedSandboxNetworkDenySpec) int {
		if a.Host != b.Host {
			return strings.Compare(a.Host, b.Host)
		}
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return 0
	})
	return normalized, nil
}

func normalizeSandboxNetworkAllow(path string, values []SandboxNetworkAllowSpec) ([]NormalizedSandboxNetworkAllowSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxSandboxNetworkAllowEntries {
		return nil, &ValidationError{Path: path, Message: fmt.Sprintf("at most %d allow entries are supported, got %d", maxSandboxNetworkAllowEntries, len(values))}
	}
	normalized := make([]NormalizedSandboxNetworkAllowSpec, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		host, err := egress.NormalizeHost(value.Host)
		if err != nil {
			return nil, &ValidationError{Path: entryPath + ".host", Message: err.Error()}
		}
		if len(value.AllowedIPs) > maxSandboxNetworkAllowAllowedIPs {
			return nil, &ValidationError{Path: entryPath + ".allowed_ips", Message: fmt.Sprintf("at most %d allowed addresses are supported per entry, got %d", maxSandboxNetworkAllowAllowedIPs, len(value.AllowedIPs))}
		}
		allowedIPs, err := egress.NormalizeAllowedIPs(value.AllowedIPs)
		if err != nil {
			return nil, &ValidationError{Path: entryPath + ".allowed_ips", Message: err.Error()}
		}
		ports, err := normalizeSandboxNetworkPorts(entryPath, value.Port, value.Ports, maxSandboxNetworkAllowEntries)
		if err != nil {
			return nil, err
		}
		for _, port := range ports {
			dedupeKey := fmt.Sprintf("%s\x00%d", host, port)
			if _, duplicate := seen[dedupeKey]; duplicate {
				return nil, &ValidationError{Path: entryPath, Message: fmt.Sprintf("duplicate allow entry for %q port %d", host, port)}
			}
			seen[dedupeKey] = struct{}{}
			normalized = append(normalized, NormalizedSandboxNetworkAllowSpec{
				Host:       host,
				Port:       port,
				AllowedIPs: append([]string(nil), allowedIPs...),
			})
		}
		if len(normalized) > maxSandboxNetworkAllowEntries {
			return nil, &ValidationError{Path: path, Message: fmt.Sprintf("at most %d allow entries are supported after expanding ports", maxSandboxNetworkAllowEntries)}
		}
	}
	// Order in the source carries no meaning because every entry allows, so
	// sort for a declaration-order-independent canonical JSON and hash.
	slices.SortFunc(normalized, func(a, b NormalizedSandboxNetworkAllowSpec) int {
		if a.Host != b.Host {
			return strings.Compare(a.Host, b.Host)
		}
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return 0
	})
	return normalized, nil
}

// normalizeSandboxNetworkPorts resolves the single-port and multi-port forms
// into the list normalization expands. Exactly one form may be used; a port
// list is deduplicated and bounded before any entry is emitted.
func normalizeSandboxNetworkPorts(path string, port int, ports []int, max int) ([]int, error) {
	if port != 0 && len(ports) > 0 {
		return nil, &ValidationError{Path: path, Message: "specify port or ports, not both"}
	}
	if len(ports) == 0 {
		if port == 0 {
			return nil, &ValidationError{Path: path + ".port", Message: "port must be between 1 and 65535, got 0"}
		}
		if port < 1 || port > 65535 {
			return nil, &ValidationError{Path: path + ".port", Message: fmt.Sprintf("port must be between 1 and 65535, got %d", port)}
		}
		return []int{port}, nil
	}
	if len(ports) > max {
		return nil, &ValidationError{Path: path + ".ports", Message: fmt.Sprintf("at most %d ports are supported per entry, got %d", max, len(ports))}
	}
	normalized := make([]int, 0, len(ports))
	seen := make(map[int]struct{}, len(ports))
	for index, candidate := range ports {
		if candidate < 1 || candidate > 65535 {
			return nil, &ValidationError{Path: fmt.Sprintf("%s.ports[%d]", path, index), Message: fmt.Sprintf("port must be between 1 and 65535, got %d", candidate)}
		}
		if _, duplicate := seen[candidate]; duplicate {
			return nil, &ValidationError{Path: path + ".ports", Message: fmt.Sprintf("duplicate port %d", candidate)}
		}
		seen[candidate] = struct{}{}
		normalized = append(normalized, candidate)
	}
	return normalized, nil
}
