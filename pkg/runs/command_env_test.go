package runs

import (
	"errors"
	"testing"

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
