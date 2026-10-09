//go:build linux && cgo && microsandboxcgo

package driver

import (
	microsandbox "github.com/superradcompany/microsandbox/sdk/go"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// microsandboxSecretOptions translates credential secret specs into the
// Microsandbox SDK's WithSecrets option.
//
// The SDK's network proxy substitutes the real value at the transport layer, so
// the credential never crosses the FFI into the guest. RequireTLSIdentity is
// pinned true: substitution over an unverified peer would hand the credential to
// whoever controls the network path, which defeats the point of keeping it out
// of the sandbox.
func microsandboxSecretOptions(specs []credentials.SecretSpec) ([]microsandbox.SandboxOption, error) {
	bindings, err := sandboxSecretBindings(specs)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}
	requireTLS := true
	entries := make([]microsandbox.SecretEntry, 0, len(bindings))
	for _, binding := range bindings {
		entries = append(entries, microsandbox.Secret.Env(binding.EnvVar, binding.Value, microsandbox.SecretEnvOptions{
			Allow:              binding.AllowHosts,
			Placeholder:        binding.Placeholder,
			RequireTLSIdentity: &requireTLS,
		}))
	}
	return []microsandbox.SandboxOption{microsandbox.WithSecrets(entries...)}, nil
}
