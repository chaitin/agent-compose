package egress

import (
	"fmt"
	"sort"
	"strings"
)

// Layer names how much of one request the engine could observe before deciding
// it. It is carried on the decision record so an operator can tell an opaque
// tunnel decision from one made on the HTTP request line.
type Layer string

const (
	// LayerL4 is a decision made on the destination alone: a CONNECT tunnel
	// the engine forwards without terminating TLS.
	LayerL4 Layer = "l4"
	// LayerL7 is a decision made on an HTTP request whose method and path the
	// engine observed, either because it is plain HTTP or because the engine
	// terminated TLS for it.
	LayerL7 Layer = "l7"
)

// Enforcement selects whether a rule's L7 constraints are enforced or only
// audited. The zero value means enforce, so a rule that does not declare it
// keeps the strict behavior.
type Enforcement string

const (
	// EnforcementEnforce denies an L7 request that does not match the rule's
	// declared methods and paths. It is the default.
	EnforcementEnforce Enforcement = "enforce"
	// EnforcementAudit allows the request but records that the L7 constraints
	// would have refused it. It is for rolling out a policy before enforcing
	// it.
	EnforcementAudit Enforcement = "audit"
)

// ParseEnforcement normalizes a declared enforcement value. An empty value is
// the default, enforce, because a declaration that says nothing must not quietly
// become observation-only.
func ParseEnforcement(value string) (Enforcement, error) {
	switch normalized := Enforcement(strings.ToLower(strings.TrimSpace(value))); normalized {
	case "", EnforcementEnforce:
		return EnforcementEnforce, nil
	case EnforcementAudit:
		return EnforcementAudit, nil
	default:
		return "", fmt.Errorf("enforcement must be %q or %q, got %q", EnforcementEnforce, EnforcementAudit, value)
	}
}

// normalizeEnforcement maps the zero value onto the default. An unrepresentable
// value is treated as enforce so a malformed rule can never widen access by
// falling back to audit.
func normalizeEnforcement(value Enforcement) Enforcement {
	if value == EnforcementAudit {
		return EnforcementAudit
	}
	return EnforcementEnforce
}

// Limits bound one allowance's L7 constraint. Every entry is validated,
// canonicalized, and compiled before a declaration is accepted, so an unbounded
// list is work a request makes the daemon do. The limits are far above a real
// declaration and exist only to bound that work.
const (
	MaxL7Methods      = 16
	MaxL7Paths        = 64
	MaxL7MethodLength = 32
	MaxL7PathLength   = 2048
)

// L7Constraint is the optional application-layer narrowing of one allowance: the
// HTTP methods and path patterns it permits. An empty constraint is L4 behavior,
// which is what keeps an allowance that does not declare methods or paths
// unchanged.
//
// Paths match the escaped request path without the query string. A pattern is
// either an exact path or a prefix ending in "*"; "*" is only accepted as the
// final character, so a declaration cannot express an ambiguous wildcard.
type L7Constraint struct {
	// Methods are uppercase HTTP method tokens. Empty permits every method.
	Methods []string `yaml:"methods,omitempty" json:"methods,omitempty"`
	// Paths are exact paths or prefix patterns ending in "*". Empty permits
	// every path.
	Paths []string `yaml:"paths,omitempty" json:"paths,omitempty"`
}

// NormalizeL7Constraint validates a declared method and path list and returns
// the canonical, sorted form. Duplicate entries are rejected rather than
// collapsed so a declaration cannot contain the same rule twice while the
// operator believes it wrote two.
func NormalizeL7Constraint(methods, paths []string) (L7Constraint, error) {
	normalizedMethods, err := normalizeL7Methods(methods)
	if err != nil {
		return L7Constraint{}, err
	}
	normalizedPaths, err := normalizeL7Paths(paths)
	if err != nil {
		return L7Constraint{}, err
	}
	return L7Constraint{Methods: normalizedMethods, Paths: normalizedPaths}, nil
}

func normalizeL7Methods(methods []string) ([]string, error) {
	if len(methods) == 0 {
		return nil, nil
	}
	if len(methods) > MaxL7Methods {
		return nil, fmt.Errorf("at most %d methods are supported, got %d", MaxL7Methods, len(methods))
	}
	normalized := make([]string, 0, len(methods))
	seen := make(map[string]struct{}, len(methods))
	for index, value := range methods {
		method := strings.ToUpper(strings.TrimSpace(value))
		if method == "" {
			return nil, fmt.Errorf("methods[%d] is empty", index)
		}
		if len(method) > MaxL7MethodLength {
			return nil, fmt.Errorf("methods[%d] is longer than %d characters", index, MaxL7MethodLength)
		}
		if !isHTTPMethodToken(method) {
			return nil, fmt.Errorf("methods[%d] %q is not a valid HTTP method", index, value)
		}
		if _, duplicate := seen[method]; duplicate {
			return nil, fmt.Errorf("duplicate method %q", method)
		}
		seen[method] = struct{}{}
		normalized = append(normalized, method)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeL7Paths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	if len(paths) > MaxL7Paths {
		return nil, fmt.Errorf("at most %d paths are supported, got %d", MaxL7Paths, len(paths))
	}
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for index, value := range paths {
		path := strings.TrimSpace(value)
		if path == "" {
			return nil, fmt.Errorf("paths[%d] is empty", index)
		}
		if len(path) > MaxL7PathLength {
			return nil, fmt.Errorf("paths[%d] is longer than %d characters", index, MaxL7PathLength)
		}
		if !strings.HasPrefix(path, "/") {
			return nil, fmt.Errorf("paths[%d] %q must start with %q", index, value, "/")
		}
		if strings.ContainsAny(path, "?#") {
			return nil, fmt.Errorf("paths[%d] %q must not contain a query or fragment", index, value)
		}
		if strings.Count(path, "*") > 1 || (strings.Contains(path, "*") && !strings.HasSuffix(path, "*")) {
			return nil, fmt.Errorf("paths[%d] %q may use %q only as its final character", index, value, "*")
		}
		for _, r := range path {
			if r < 0x20 || r == 0x7f || r == ' ' {
				return nil, fmt.Errorf("paths[%d] %q must not contain whitespace or control characters", index, value)
			}
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("duplicate path %q", path)
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	sort.Strings(normalized)
	return normalized, nil
}

// isHTTPMethodToken reports whether value is an RFC 9110 token. It is checked
// rather than trusted because the method is matched against the request line,
// which a client controls.
func isHTTPMethodToken(value string) bool {
	for _, r := range value {
		switch {
		case r >= 'A' && r <= 'Z',
			r >= 'a' && r <= 'z',
			r >= '0' && r <= '9':
			continue
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
			continue
		default:
			return false
		}
	}
	return value != ""
}

// Empty reports whether the constraint narrows nothing, which is the L4
// behavior an allowance without methods or paths keeps.
func (c L7Constraint) Empty() bool {
	return len(c.Methods) == 0 && len(c.Paths) == 0
}

// Clone returns a deep copy so a policy or declaration cannot share its backing
// arrays with the value it was copied from.
func (c L7Constraint) Clone() L7Constraint {
	return L7Constraint{
		Methods: append([]string(nil), c.Methods...),
		Paths:   append([]string(nil), c.Paths...),
	}
}

// Matches reports whether method and path satisfy the constraint. An empty
// constraint matches everything. A constraint that names methods or paths
// matches only a request that carries both values: a CONNECT the engine did not
// inspect cannot silently satisfy a declaration that asked for inspection.
func (c L7Constraint) Matches(method, path string) bool {
	if c.Empty() {
		return true
	}
	if len(c.Methods) > 0 {
		normalizedMethod := strings.ToUpper(strings.TrimSpace(method))
		matched := false
		for _, candidate := range c.Methods {
			if candidate == normalizedMethod {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(c.Paths) > 0 {
		requestPath := path
		if requestPath == "" {
			requestPath = "/"
		}
		matched := false
		for _, candidate := range c.Paths {
			if strings.HasSuffix(candidate, "*") {
				if strings.HasPrefix(requestPath, strings.TrimSuffix(candidate, "*")) {
					matched = true
					break
				}
				continue
			}
			if candidate == requestPath {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
