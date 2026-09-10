package main

import agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"

type composeWorkspaceDeliveryOutput struct {
	Mode       string `json:"mode"`
	SourcePath string `json:"source_path,omitempty"`
	Target     string `json:"target,omitempty"`
	ReadOnly   bool   `json:"read_only"`
}

func composeWorkspaceDeliveryFromProto(delivery *agentcomposev2.SandboxWorkspaceDelivery) *composeWorkspaceDeliveryOutput {
	if delivery == nil {
		return nil
	}
	mode := "unknown"
	switch delivery.GetMode() {
	case agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY:
		mode = "copy"
	case agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT:
		mode = "mount"
	case agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED:
	}
	return &composeWorkspaceDeliveryOutput{
		Mode: mode, SourcePath: delivery.GetSourcePath(),
		Target: delivery.GetTarget(), ReadOnly: delivery.GetReadOnly(),
	}
}
