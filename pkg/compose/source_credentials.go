package compose

import (
	"fmt"

	"github.com/chaitin/agent-compose/pkg/sources"
)

// SourceCredentialMode identifies whether persisted source credentials still
// use authoring-time environment references or were resolved before transport.
type SourceCredentialMode uint8

const (
	// SourceCredentialsFromReferences requires password and token fields to use
	// environment references and resolves all credential references from
	// NormalizeOptions.Env. References that cannot be resolved from the
	// environment are kept as-is so authoring input remains valid without the
	// variable present; the source consumer determines whether deferred
	// resolution is allowed and which explicit environment scope it uses.
	SourceCredentialsFromReferences SourceCredentialMode = iota
	// SourceCredentialsResolved accepts credential values that the CLI already
	// resolved as well as legacy environment references persisted by older
	// daemons. Consumers must reject unresolved references or resolve them
	// against an explicit scope, never the daemon process environment.
	SourceCredentialsResolved
)

// WorkspaceCredentialMode is retained for source compatibility.
// Deprecated: use SourceCredentialMode.
type WorkspaceCredentialMode = SourceCredentialMode

const (
	// Deprecated: use SourceCredentialsFromReferences.
	WorkspaceCredentialsFromReferences = SourceCredentialsFromReferences
	// Deprecated: use SourceCredentialsResolved.
	WorkspaceCredentialsResolved = SourceCredentialsResolved
)

func normalizeSourceCredentials(path string, source sources.Source, options NormalizeOptions) (sources.Source, error) {
	switch options.SourceCredentials {
	case SourceCredentialsFromReferences:
		if err := validateSourceSecrets(path, source); err != nil {
			return sources.Source{}, err
		}
		return resolveSourceCredentialReferences(path, source, options)
	case SourceCredentialsResolved:
		return source, nil
	default:
		return sources.Source{}, fmt.Errorf("unsupported source credential mode %d", options.SourceCredentials)
	}
}

// resolveSourceCredentialReferences resolves each credential reference from
// NormalizeOptions.Env. A reference whose variable is missing from the
// environment is kept as-is instead of failing: authoring input may rely on
// deferred resolution by a scoped consumer, and persisted legacy data may still
// contain references. Literal credential values are never rewritten.
func resolveSourceCredentialReferences(path string, source sources.Source, options NormalizeOptions) (sources.Source, error) {
	var err error
	source.Username, err = interpolateEnvValueLoose(path+".username", source.Username, options)
	if err != nil {
		return sources.Source{}, err
	}
	source.Password, err = interpolateEnvValueLoose(path+".password", source.Password, options)
	if err != nil {
		return sources.Source{}, err
	}
	source.Token, err = interpolateEnvValueLoose(path+".token", source.Token, options)
	if err != nil {
		return sources.Source{}, err
	}
	return source.Normalized(), nil
}
