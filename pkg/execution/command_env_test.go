package execution

import (
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestValidateCommandEnvRejectsSecretWithoutLeakingValue(t *testing.T) {
	const secret = "must-not-appear-in-error"
	err := ValidateCommandEnv([]*agentcomposev2.EnvVarSpec{
		{Name: " API_TOKEN ", Value: secret},
		{Name: "REQUEST_TOKEN", Value: secret, Secret: true},
	})
	if err == nil {
		t.Fatal("ValidateCommandEnv returned nil for secret env")
	}
	if !errors.Is(err, domain.ErrInvalidArgument) || !errors.Is(err, ErrSecretCommandEnvUnsupported) {
		t.Fatalf("ValidateCommandEnv error = %v, want invalid-argument secret-env error", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("ValidateCommandEnv leaked secret value in error: %v", err)
	}
	if !strings.Contains(err.Error(), "secret_command_env_unsupported") || !strings.Contains(err.Error(), "REQUEST_TOKEN") {
		t.Fatalf("ValidateCommandEnv error = %v, want variable name", err)
	}
}

func TestValidateCommandEnvAllowsUnmarkedAndNilEntries(t *testing.T) {
	if err := ValidateCommandEnv([]*agentcomposev2.EnvVarSpec{
		nil,
		{Name: "REQUEST_TOKEN", Value: "value"},
	}); err != nil {
		t.Fatalf("ValidateCommandEnv returned error for ordinary env: %v", err)
	}
}

func TestValidateSandboxCommandEnvRejectsSecret(t *testing.T) {
	if err := ValidateSandboxCommandEnv([]domain.SandboxEnvVar{{Name: "TOKEN", Value: "secret", Secret: true}}); err == nil || !errors.Is(err, ErrSecretCommandEnvUnsupported) {
		t.Fatalf("ValidateSandboxCommandEnv error = %v, want secret-env rejection", err)
	}
	if err := ValidateSandboxCommandEnv([]domain.SandboxEnvVar{{Name: "MODE", Value: "review"}}); err != nil {
		t.Fatalf("ValidateSandboxCommandEnv rejected ordinary env: %v", err)
	}
}
