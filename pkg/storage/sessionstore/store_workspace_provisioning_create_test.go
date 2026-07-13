package sessionstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestStoreCreateSandboxInitializesWorkspaceProvisioning(t *testing.T) {
	tests := []struct {
		name        string
		withOptions bool
		workspaceID string
		workspace   *SandboxWorkspace
	}{
		{
			name:        "workspace snapshot",
			withOptions: true,
			workspace: &SandboxWorkspace{
				Name:       "snapshot",
				Type:       "file",
				ConfigJSON: `{"path":"fixture"}`,
			},
		},
		{
			name:      "empty workspace snapshot",
			workspace: &SandboxWorkspace{},
		},
		{
			name: "workspace snapshot ID",
			workspace: &SandboxWorkspace{
				ID: "snapshot-id",
			},
		},
		{
			name:        "workspace ID only",
			withOptions: true,
			workspaceID: "workspace-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newCoverageStore(t)
			created := createSandboxForProvisioningTest(
				t,
				store,
				tt.withOptions,
				tt.workspaceID,
				tt.workspace,
			)

			assertPendingWorkspaceProvisioning(t, "created sandbox", created.WorkspaceProvisioning)
			persisted, rawMetadata := readProvisioningTestMetadata(t, store, created.Summary.ID)
			assertPendingWorkspaceProvisioning(t, "persisted sandbox", persisted.WorkspaceProvisioning)
			if _, ok := rawMetadata["workspace_provisioning"]; !ok {
				t.Fatal("metadata.json has no workspace_provisioning field")
			}
		})
	}
}

func TestStoreCreateSandboxWithoutWorkspaceOmitsWorkspaceProvisioning(t *testing.T) {
	store := newCoverageStore(t)
	created := createSandboxForProvisioningTest(t, store, false, "", nil)
	if created.WorkspaceProvisioning != nil {
		t.Fatalf("created sandbox workspace provisioning = %#v, want nil", created.WorkspaceProvisioning)
	}

	persisted, rawMetadata := readProvisioningTestMetadata(t, store, created.Summary.ID)
	if persisted.WorkspaceProvisioning != nil {
		t.Fatalf("persisted sandbox workspace provisioning = %#v, want nil", persisted.WorkspaceProvisioning)
	}
	if _, ok := rawMetadata["workspace_provisioning"]; ok {
		t.Fatalf("metadata.json contains workspace_provisioning: %s", rawMetadata["workspace_provisioning"])
	}
}

func createSandboxForProvisioningTest(
	t *testing.T,
	store *Store,
	withOptions bool,
	workspaceID string,
	workspace *SandboxWorkspace,
) *Sandbox {
	t.Helper()
	ctx := context.Background()
	if withOptions {
		created, err := store.CreateSandboxWithOptions(
			ctx,
			"workspace provisioning",
			"",
			driverpkg.RuntimeDriverBoxlite,
			"",
			workspaceID,
			"",
			workspace,
			nil,
			nil,
			CreateSandboxOptions{},
		)
		if err != nil {
			t.Fatalf("CreateSandboxWithOptions returned error: %v", err)
		}
		return created
	}

	created, err := store.CreateSandbox(
		ctx,
		"workspace provisioning",
		"",
		driverpkg.RuntimeDriverBoxlite,
		"",
		workspaceID,
		"",
		workspace,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	return created
}

func readProvisioningTestMetadata(
	t *testing.T,
	store *Store,
	sandboxID string,
) (*Sandbox, map[string]json.RawMessage) {
	t.Helper()
	path := filepath.Join(store.SandboxDir(sandboxID), "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metadata.json: %v", err)
	}
	var persisted Sandbox
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("decode metadata.json as sandbox: %v", err)
	}
	var rawMetadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMetadata); err != nil {
		t.Fatalf("decode metadata.json fields: %v", err)
	}
	return &persisted, rawMetadata
}

func assertPendingWorkspaceProvisioning(
	t *testing.T,
	label string,
	provisioning *domain.SandboxWorkspaceProvisioning,
) {
	t.Helper()
	if provisioning == nil {
		t.Fatalf("%s workspace provisioning = nil, want pending", label)
	}
	if provisioning.Version != domain.SandboxWorkspaceProvisioningVersion {
		t.Errorf(
			"%s workspace provisioning version = %d, want %d",
			label,
			provisioning.Version,
			domain.SandboxWorkspaceProvisioningVersion,
		)
	}
	if provisioning.Status != domain.SandboxWorkspaceProvisioningStatusPending {
		t.Errorf(
			"%s workspace provisioning status = %q, want %q",
			label,
			provisioning.Status,
			domain.SandboxWorkspaceProvisioningStatusPending,
		)
	}
	if provisioning.UpdatedAt.IsZero() {
		t.Errorf("%s workspace provisioning updated_at is zero", label)
	}
	if provisioning.UpdatedAt.Location() != time.UTC {
		t.Errorf(
			"%s workspace provisioning updated_at location = %s, want UTC",
			label,
			provisioning.UpdatedAt.Location(),
		)
	}
}
