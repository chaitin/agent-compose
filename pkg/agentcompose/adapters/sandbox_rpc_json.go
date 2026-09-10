package adapters

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
)

type sandboxRPCIDRequest struct {
	SandboxID string `json:"sandboxId"`
}

func (r sandboxRPCIDRequest) ID() string {
	return strings.TrimSpace(r.SandboxID)
}

type sandboxRPCCreateRequest struct {
	Title         string                 `json:"title"`
	BaseWorkspace string                 `json:"baseWorkspace"`
	Tags          []domain.SandboxTag    `json:"tags"`
	EnvItems      []domain.SandboxEnvVar `json:"envItems"`
	WorkspaceID   string                 `json:"workspaceId"`
	GuestImage    string                 `json:"guestImage"`
	Driver        string                 `json:"driver"`
	CapsetIDs     []string               `json:"capsetIds"`
}

type sandboxRPCListRequest struct {
	SandboxType   string `json:"sandboxType"`
	TriggerSource string `json:"triggerSourceQuery"`
	Title         string `json:"titleQuery"`
	Workspace     string `json:"workspaceQuery"`
	Driver        string `json:"driver"`
	VMStatus      string `json:"vmStatus"`
	CreatedFrom   string `json:"createdFrom"`
	CreatedTo     string `json:"createdTo"`
	UpdatedFrom   string `json:"updatedFrom"`
	UpdatedTo     string `json:"updatedTo"`
	Offset        uint32 `json:"offset"`
	Limit         uint32 `json:"limit"`
}

func (r sandboxRPCListRequest) Options() (domain.SandboxListOptions, error) {
	vmStatus, err := sandboxes.NormalizeVMStatus(r.VMStatus)
	if err != nil {
		return domain.SandboxListOptions{}, err
	}
	createdFrom, err := sandboxRPCOptionalTime(r.CreatedFrom, "createdFrom")
	if err != nil {
		return domain.SandboxListOptions{}, err
	}
	createdTo, err := sandboxRPCOptionalTime(r.CreatedTo, "createdTo")
	if err != nil {
		return domain.SandboxListOptions{}, err
	}
	updatedFrom, err := sandboxRPCOptionalTime(r.UpdatedFrom, "updatedFrom")
	if err != nil {
		return domain.SandboxListOptions{}, err
	}
	updatedTo, err := sandboxRPCOptionalTime(r.UpdatedTo, "updatedTo")
	if err != nil {
		return domain.SandboxListOptions{}, err
	}
	return domain.SandboxListOptions{
		SandboxType: r.SandboxType, TriggerSourceQuery: r.TriggerSource, TitleQuery: r.Title,
		WorkspaceQuery: r.Workspace, Driver: r.Driver, VMStatus: vmStatus,
		CreatedFrom: createdFrom, CreatedTo: createdTo, UpdatedFrom: updatedFrom, UpdatedTo: updatedTo,
		Offset: int(r.Offset), Limit: int(r.Limit),
	}, nil
}

func sandboxRPCOptionalTime(value, field string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", field, err)
	}
	parsed = parsed.UTC()
	return parsed, nil
}

type sandboxRPCResponse struct {
	Sandbox *sandboxRPCDetail `json:"sandbox,omitempty"`
}

type sandboxRPCDetail struct {
	Summary     *sandboxRPCSummary     `json:"summary,omitempty"`
	EnvItems    []domain.SandboxEnvVar `json:"envItems,omitempty"`
	WorkspaceID string                 `json:"workspaceId,omitempty"`
	Workspace   *sandboxRPCWorkspace   `json:"workspace,omitempty"`
}

// The RPC contract projects public workspace fields explicitly. The domain
// model also persists transient ownership and holds a process-local lease.
type sandboxRPCWorkspace struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	ConfigJSON string `json:"config_json,omitempty"`
}

type sandboxRPCSummary struct {
	SandboxID     string              `json:"sandboxId"`
	Title         string              `json:"title,omitempty"`
	Driver        string              `json:"driver,omitempty"`
	VMStatus      string              `json:"vmStatus,omitempty"`
	WorkspacePath string              `json:"workspacePath,omitempty"`
	ProxyPath     string              `json:"proxyPath,omitempty"`
	CreatedAt     string              `json:"createdAt,omitempty"`
	UpdatedAt     string              `json:"updatedAt,omitempty"`
	CellCount     uint32              `json:"cellCount,omitempty"`
	EventCount    uint32              `json:"eventCount,omitempty"`
	Tags          []domain.SandboxTag `json:"tags,omitempty"`
	GuestImage    string              `json:"guestImage,omitempty"`
	TriggerSource string              `json:"triggerSource,omitempty"`
}

type sandboxRPCListResponse struct {
	Sandboxes  []*sandboxRPCSummary `json:"sandboxes,omitempty"`
	TotalCount uint32               `json:"totalCount,omitempty"`
	HasMore    bool                 `json:"hasMore,omitempty"`
	NextOffset uint32               `json:"nextOffset,omitempty"`
}

type sandboxRPCProxyResponse struct {
	SandboxID   string `json:"sandboxId"`
	ProxyPath   string `json:"proxyPath,omitempty"`
	NotebookURL string `json:"notebookUrl,omitempty"`
	Driver      string `json:"driver,omitempty"`
	VMStatus    string `json:"vmStatus,omitempty"`
}

func sandboxRPCDetailFromDomain(sandbox *domain.Sandbox) *sandboxRPCDetail {
	if sandbox == nil {
		return nil
	}
	env := make([]domain.SandboxEnvVar, 0, len(sandbox.EnvItems))
	for _, item := range sandbox.EnvItems {
		if item.Secret && item.Value != "" {
			item.Value = "********"
		}
		env = append(env, item)
	}
	var workspace *sandboxRPCWorkspace
	if source := sandbox.Workspace; source != nil {
		workspace = &sandboxRPCWorkspace{ID: source.ID, Name: source.Name, Type: source.Type, ConfigJSON: source.ConfigJSON}
	}
	return &sandboxRPCDetail{Summary: sandboxRPCSummaryFromDomain(&sandbox.Summary), EnvItems: env, WorkspaceID: sandbox.WorkspaceID, Workspace: workspace}
}

func sandboxRPCSummaryFromDomain(summary *domain.SandboxSummary) *sandboxRPCSummary {
	if summary == nil {
		return nil
	}
	return &sandboxRPCSummary{
		SandboxID: summary.ID, Title: summary.Title, Driver: summary.Driver, VMStatus: summary.VMStatus,
		WorkspacePath: summary.WorkspacePath, ProxyPath: summary.ProxyPath,
		CreatedAt: summary.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: summary.UpdatedAt.Format(time.RFC3339Nano),
		CellCount: uint32(summary.CellCount), EventCount: uint32(summary.EventCount), Tags: summary.Tags,
		GuestImage: summary.GuestImage, TriggerSource: summary.TriggerSource,
	}
}

func decodeSandboxRPCJSON(raw string, target any) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode sandbox rpc request: %w", err)
	}
	return nil
}

func encodeSandboxRPCJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode sandbox rpc response: %w", err)
	}
	return string(data), nil
}
