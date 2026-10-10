package compose

import (
	"fmt"
	"slices"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// Sandbox network policy defaults. DefaultAllowAll is the value an omitted
// default normalizes to; it is not an engine default, because the whole block
// has to be declared before any of this applies (D3).
const (
	SandboxNetworkDefaultAllowAll = "allow-all"
	SandboxNetworkDefaultDeny     = "deny"
)

// NormalizeSandboxNetworkSpec validates a declared sandbox network policy and
// returns its normalized form, or nil when the block was not declared.
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
	return &NormalizedSandboxNetworkSpec{Default: defaultAction, Allow: allow}, nil
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
			Host:     entry.Host,
			Port:     entry.Port,
			Protocol: egress.Protocol(entry.Protocol),
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
	if normalized == SandboxNetworkDefaultDeny {
		return egress.Deny, nil
	}
	return egress.Allow, nil
}

func normalizeSandboxNetworkDefault(value string) (string, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "":
		return SandboxNetworkDefaultAllowAll, nil
	case SandboxNetworkDefaultAllowAll, SandboxNetworkDefaultDeny:
		return normalized, nil
	default:
		return "", fmt.Errorf("network default must be %q or %q", SandboxNetworkDefaultAllowAll, SandboxNetworkDefaultDeny)
	}
}

func normalizeSandboxNetworkAllow(path string, values []SandboxNetworkAllowSpec) ([]NormalizedSandboxNetworkAllowSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]NormalizedSandboxNetworkAllowSpec, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		entryPath := fmt.Sprintf("%s[%d]", path, index)
		host := strings.ToLower(strings.TrimSpace(value.Host))
		if err := egress.ValidateHostPattern(host); err != nil {
			return nil, &ValidationError{Path: entryPath + ".host", Message: err.Error()}
		}
		if value.Port < 1 || value.Port > 65535 {
			return nil, &ValidationError{Path: entryPath + ".port", Message: fmt.Sprintf("port must be between 1 and 65535, got %d", value.Port)}
		}
		protocol, err := egress.ParseProtocol(value.Protocol)
		if err != nil {
			return nil, &ValidationError{Path: entryPath + ".protocol", Message: err.Error()}
		}
		dedupeKey := fmt.Sprintf("%s\x00%d\x00%s", host, value.Port, protocol)
		if _, duplicate := seen[dedupeKey]; duplicate {
			return nil, &ValidationError{Path: entryPath, Message: fmt.Sprintf("duplicate allow entry for %q port %d protocol %q", host, value.Port, protocol)}
		}
		seen[dedupeKey] = struct{}{}
		normalized = append(normalized, NormalizedSandboxNetworkAllowSpec{
			Host:     host,
			Port:     value.Port,
			Protocol: string(protocol),
		})
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
		return strings.Compare(a.Protocol, b.Protocol)
	})
	return normalized, nil
}
