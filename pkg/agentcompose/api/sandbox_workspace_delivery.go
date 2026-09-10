package api

import (
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// Lightweight listing records omit ConfigJSON. Do not infer copy from those
// incomplete records, and never expose source credentials or managed roots.
func sandboxWorkspaceDeliveryToProto(workspace *domain.SandboxWorkspace) *agentcomposev2.SandboxWorkspaceDelivery {
	if workspace == nil || strings.TrimSpace(workspace.ConfigJSON) == "" {
		return nil
	}
	mount, err := workspaces.DecodeFileWorkspaceMount(workspace.ConfigJSON)
	if err != nil {
		return &agentcomposev2.SandboxWorkspaceDelivery{}
	}
	if mount == nil {
		return &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY}
	}
	if !strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		return &agentcomposev2.SandboxWorkspaceDelivery{}
	}
	return &agentcomposev2.SandboxWorkspaceDelivery{
		Mode:       agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT,
		SourcePath: mount.SourcePath,
		Target:     mount.Target,
		ReadOnly:   mount.ReadOnly,
	}
}
