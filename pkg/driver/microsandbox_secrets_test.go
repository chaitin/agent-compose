//go:build linux && cgo && microsandboxcgo

package driver

import (
	"testing"

	microsandbox "github.com/superradcompany/microsandbox/sdk/go"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// TestMicrosandboxSecretOptionsCarryHostScopeAndTLS pins the exact SDK shape the
// guest's isolation depends on: the real value stays in the SecretEntry, the
// guest address is the placeholder, substitution is limited to the scoped host,
// and a verified TLS identity is required.
func TestMicrosandboxSecretOptionsCarryHostScopeAndTLS(t *testing.T) {
	options, err := microsandboxSecretOptions([]credentials.SecretSpec{{
		Name:        "GITHUB_TOKEN",
		Value:       "credential-truth",
		Placeholder: "ac_ph_fingerprint",
		AllowHosts:  []string{"api.github.com"},
		RequireTLS:  true,
	}})
	if err != nil {
		t.Fatalf("microsandboxSecretOptions() error = %v", err)
	}
	if len(options) != 1 {
		t.Fatalf("options = %d, want exactly one WithSecrets option", len(options))
	}
	config := &microsandbox.SandboxConfig{}
	options[0](config)
	if len(config.Secrets) != 1 {
		t.Fatalf("secrets = %d, want 1", len(config.Secrets))
	}
	secret := config.Secrets[0]
	if secret.EnvVar != "GITHUB_TOKEN" || secret.Value != "credential-truth" {
		t.Fatalf("secret = %#v, want the declared variable and the credential truth", secret)
	}
	if secret.Placeholder != "ac_ph_fingerprint" {
		t.Fatalf("Placeholder = %q, want the fingerprint placeholder the guest sees", secret.Placeholder)
	}
	if len(secret.Allow) != 1 || secret.Allow[0] != "api.github.com" {
		t.Fatalf("Allow = %#v, want only the scoped host", secret.Allow)
	}
	if secret.RequireTLSIdentity == nil || !*secret.RequireTLSIdentity {
		t.Fatal("RequireTLSIdentity must be set true so substitution needs a verified TLS peer")
	}
}

func TestMicrosandboxSecretOptionsRefuseUnsafeSpecs(t *testing.T) {
	if _, err := microsandboxSecretOptions([]credentials.SecretSpec{{
		Name:        "GITHUB_TOKEN",
		Value:       "credential-truth",
		Placeholder: "ac_ph",
		AllowHosts:  []string{"api.github.com"},
	}}); err == nil {
		t.Fatal("microsandboxSecretOptions() must refuse a spec that does not require TLS")
	}
	if options, err := microsandboxSecretOptions(nil); err != nil || options != nil {
		t.Fatalf("microsandboxSecretOptions(nil) = %#v, %v; want nil, nil", options, err)
	}
}
