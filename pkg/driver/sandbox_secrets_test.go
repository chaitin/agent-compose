package driver

import (
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

func TestSandboxSecretBindingsNormalizesSpecs(t *testing.T) {
	bindings, err := sandboxSecretBindings([]credentials.SecretSpec{
		{
			Name:        " GIT_TOKEN ",
			Value:       "secret-truth",
			Placeholder: "ac_ph_fp",
			AllowHosts:  []string{" git.example.com ", ""},
			RequireTLS:  true,
		},
	})
	if err != nil {
		t.Fatalf("sandboxSecretBindings() error = %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want one", bindings)
	}
	binding := bindings[0]
	if binding.EnvVar != "GIT_TOKEN" {
		t.Fatalf("EnvVar = %q, want the trimmed name", binding.EnvVar)
	}
	if len(binding.AllowHosts) != 1 || binding.AllowHosts[0] != "git.example.com" {
		t.Fatalf("AllowHosts = %#v, want blank hosts dropped", binding.AllowHosts)
	}
	if !binding.RequireTLS {
		t.Fatal("RequireTLS must survive normalization as true")
	}
}

func TestSandboxSecretBindingsRejectsUnsafeSpecs(t *testing.T) {
	base := credentials.SecretSpec{
		Name:        "GIT_TOKEN",
		Value:       "secret-truth",
		Placeholder: "ac_ph_fp",
		AllowHosts:  []string{"git.example.com"},
		RequireTLS:  true,
	}
	tests := []struct {
		name string
		spec credentials.SecretSpec
	}{
		{name: "TLS not required", spec: credentials.SecretSpec{Name: base.Name, Value: base.Value, Placeholder: base.Placeholder, AllowHosts: base.AllowHosts}},
		{name: "no allowed host", spec: credentials.SecretSpec{Name: base.Name, Value: base.Value, Placeholder: base.Placeholder, RequireTLS: true}},
		{name: "blank allowed host", spec: credentials.SecretSpec{Name: base.Name, Value: base.Value, Placeholder: base.Placeholder, AllowHosts: []string{"  "}, RequireTLS: true}},
		{name: "missing name", spec: credentials.SecretSpec{Value: base.Value, Placeholder: base.Placeholder, AllowHosts: base.AllowHosts, RequireTLS: true}},
		{name: "missing placeholder", spec: credentials.SecretSpec{Name: base.Name, Value: base.Value, AllowHosts: base.AllowHosts, RequireTLS: true}},
		{name: "placeholder with newline", spec: credentials.SecretSpec{Name: base.Name, Value: base.Value, Placeholder: "bad\nplaceholder", AllowHosts: base.AllowHosts, RequireTLS: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := sandboxSecretBindings([]credentials.SecretSpec{test.spec}); !errors.Is(err, credentials.ErrInvalidSecretSpec) {
				t.Fatalf("sandboxSecretBindings() error = %v, want ErrInvalidSecretSpec", err)
			}
		})
	}
}

func TestSandboxSecretBindingsRejectsDuplicateEnvironmentVariables(t *testing.T) {
	spec := credentials.SecretSpec{
		Name:        "GIT_TOKEN",
		Value:       "secret-truth",
		Placeholder: "ac_ph_fp",
		AllowHosts:  []string{"git.example.com"},
		RequireTLS:  true,
	}
	if _, err := sandboxSecretBindings([]credentials.SecretSpec{spec, spec}); !errors.Is(err, credentials.ErrInvalidSecretSpec) {
		t.Fatalf("sandboxSecretBindings() duplicate error = %v, want ErrInvalidSecretSpec", err)
	}
}

func TestSandboxSecretBindingsEmptyForNoSpecs(t *testing.T) {
	bindings, err := sandboxSecretBindings(nil)
	if err != nil || bindings != nil {
		t.Fatalf("sandboxSecretBindings(nil) = %#v, %v; want nil, nil", bindings, err)
	}
}
