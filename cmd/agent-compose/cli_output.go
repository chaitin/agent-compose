package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"fmt"
	"io"
)

func writeCommandOutput(out io.Writer, data []byte) error {
	if _, err := out.Write(data); err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	_, err := fmt.Fprintln(out)
	return err
}

type composeDisplayChangeOutput struct {
	Action       string
	ResourceType string
	ID           string
	Name         string
	Owner        string
	Message      string
}

type composePSOutput struct {
	Project   composeUpProjectOutput   `json:"project"`
	Sandboxes []composePSSandboxOutput `json:"sandboxes"`
}

type composeWorkspaceReclamationOutput struct {
	State       string `json:"state"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
}

func composeChangeOutputs(changes []*agentcomposev2.ProjectChange) []composeUpChangeOutput {
	output := make([]composeUpChangeOutput, 0, len(changes))
	for _, change := range changes {
		output = append(output, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ID:           displayOpaqueID(change.GetResourceId()),
			ShortID:      shortOpaqueID(change.GetResourceId()),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return output
}
