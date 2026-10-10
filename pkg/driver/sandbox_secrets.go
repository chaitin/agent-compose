package driver

import (
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// sandboxSecretBinding is the normalized, driver-agnostic form both microVM
// secret adapters consume.
//
// Normalizing here rather than inside each adapter keeps BoxLite and
// Microsandbox from drifting on what "allowed host" or "require TLS" means, and
// it is testable without an SDK or cgo. The binding still carries the credential
// truth, so it must never be logged or persisted.
type sandboxSecretBinding struct {
	EnvVar      string
	Value       string
	Placeholder string
	AllowHosts  []string
	RequireTLS  bool
}

// sandboxSecretBindings validates and normalizes credential secret specs for a
// microVM secret mechanism.
//
// It refuses a duplicate environment variable: two secrets writing the same
// guest variable would make substitution order decide which credential applies,
// which is exactly the ambiguity the handle model exists to remove.
func sandboxSecretBindings(specs []credentials.SecretSpec) ([]sandboxSecretBinding, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	bindings := make([]sandboxSecretBinding, 0, len(specs))
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		normalized := normalizeSecretSpec(spec)
		if err := normalized.Validate(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[normalized.Name]; duplicate {
			return nil, fmt.Errorf("%w: duplicate secret environment variable %q", credentials.ErrInvalidSecretSpec, normalized.Name)
		}
		seen[normalized.Name] = struct{}{}
		bindings = append(bindings, sandboxSecretBinding{
			EnvVar:      normalized.Name,
			Value:       normalized.Value,
			Placeholder: normalized.Placeholder,
			AllowHosts:  normalized.AllowHosts,
			RequireTLS:  normalized.RequireTLS,
		})
	}
	return bindings, nil
}

// normalizeSecretSpec trims a spec without mutating the caller's value. Blank
// allowed hosts are dropped rather than silently matched, because an empty host
// pattern in a substitution engine is more likely to mean "any host" than
// "no host".
func normalizeSecretSpec(spec credentials.SecretSpec) credentials.SecretSpec {
	spec.Name = strings.TrimSpace(spec.Name)
	hosts := make([]string, 0, len(spec.AllowHosts))
	for _, host := range spec.AllowHosts {
		if trimmed := strings.TrimSpace(host); trimmed != "" {
			hosts = append(hosts, trimmed)
		}
	}
	spec.AllowHosts = hosts
	return spec
}
