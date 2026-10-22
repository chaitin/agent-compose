package runs

import (
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestRunProjectAgentRejectsSecretCommandEnvBeforeCreatingRun(t *testing.T) {
	fixture := newControllerRunFixture(t)
	_, execErr, err := fixture.controller.RunProjectAgent(fixture.ctx, RunAgentRequest{
		ProjectID:       "project-1",
		AgentName:       "worker",
		Command:         "echo command",
		Env:             []*agentcomposev2.EnvVarSpec{{Name: "REQUEST_TOKEN", Value: "secret", Secret: true}},
		ClientRequestID: "secret-command-env",
	}, nil)
	if !errors.Is(err, ErrInvalidRequest) || execErr != nil {
		t.Fatalf("RunProjectAgent err=%v execErr=%v, want invalid request before execution", err, execErr)
	}
	if len(fixture.configDB.runs) != 0 {
		t.Fatalf("secret command env created runs: %#v", fixture.configDB.runs)
	}
}

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
