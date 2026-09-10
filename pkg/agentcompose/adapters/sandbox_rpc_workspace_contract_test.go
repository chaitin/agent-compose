package adapters

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestSandboxRPCWorkspacePreservesCompletePublicContract(t *testing.T) {
	fields := map[string]bool{"ID": true, "Name": true, "Type": true, "ConfigJSON": true, "SnapshotID": false, "SnapshotLease": false}
	domainType := reflect.TypeFor[domain.SandboxWorkspace]()
	if domainType.NumField() != len(fields) {
		t.Fatal("review every new workspace field's RPC visibility")
	}
	rpcType := reflect.TypeFor[sandboxRPCWorkspace]()
	if rpcType.NumField() != 4 {
		t.Fatal("public workspace JSON contract changed")
	}
	for i := 0; i < domainType.NumField(); i++ {
		field := domainType.Field(i)
		public, known := fields[field.Name]
		if !known {
			t.Fatalf("workspace RPC visibility unspecified for %s", field.Name)
		}
		projected, exists := rpcType.FieldByName(field.Name)
		if exists != public {
			t.Fatalf("unexpected RPC visibility for %s", field.Name)
		}
		if public && (projected.Type != field.Type || projected.Tag.Get("json") != field.Tag.Get("json")) {
			t.Fatalf("public field %s changed type or JSON spelling", field.Name)
		}
	}
	lease := io.NopCloser(strings.NewReader("private lease"))
	workspace := &domain.SandboxWorkspace{ID: "logical", Name: "Source", Type: "file", ConfigJSON: `{"mode":"mount","source_path":"/project","target":"inputs","read_only":true}`, SnapshotID: "internal-generation", SnapshotLease: lease}
	detail := sandboxRPCDetailFromDomain(&domain.Sandbox{Workspace: workspace})
	data, err := json.Marshal(detail.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	const expected = `{"id":"logical","name":"Source","type":"file","config_json":"{\"mode\":\"mount\",\"source_path\":\"/project\",\"target\":\"inputs\",\"read_only\":true}"}`
	if string(data) != expected {
		t.Fatalf("public workspace JSON\ngot:  %s\nwant: %s", data, expected)
	}
	if workspace.SnapshotID != "internal-generation" || workspace.SnapshotLease != lease {
		t.Fatal("RPC projection mutated persisted ownership or active lease")
	}
	// Persistence still retains the ownership needed after daemon restart.
	persisted, err := json.Marshal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var restored domain.SandboxWorkspace
	if err := json.Unmarshal(persisted, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.SnapshotID != workspace.SnapshotID || restored.SnapshotLease != nil {
		t.Fatal("RPC change altered ownership/lease persistence")
	}
	for _, input := range []*domain.SandboxWorkspace{nil, {}} {
		projected := sandboxRPCDetailFromDomain(&domain.Sandbox{Workspace: input})
		output, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		var encoded map[string]json.RawMessage
		if err := json.Unmarshal(output, &encoded); err != nil {
			t.Fatal(err)
		}
		if input == nil {
			if _, exists := encoded["workspace"]; exists {
				t.Fatal("absent workspace no longer omitted")
			}
		} else if string(encoded["workspace"]) != `{"id":""}` {
			t.Fatalf("empty workspace contract changed: %s", output)
		}
	}
}

func TestSandboxRPCRequestsRejectInternalWorkspaceOwnership(t *testing.T) {
	bridge := NewSandboxRPCBridge(SandboxRPCBridgeDeps{})
	// All methods decode their public DTO before resolving stores or runtimes.
	// Create accepts an existing workspaceId, never a domain workspace object.
	for _, method := range []string{"CreateSandbox", "ResumeSandbox", "StopSandbox", "GetSandbox", "ListSandboxes", "GetSandboxProxy"} {
		for _, field := range []string{"snapshot_id", "SnapshotID", "snapshot_lease", "SnapshotLease", "workspace"} {
			t.Run(method+"/"+field, func(t *testing.T) {
				payload, err := json.Marshal(map[string]any{field: map[string]string{"snapshot_id": "forged-generation"}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = bridge.CallJSON(context.Background(), method, string(payload))
				if err == nil || !strings.Contains(err.Error(), "unknown field") {
					t.Fatalf("internal ownership input was not rejected during decoding: %v", err)
				}
			})
		}
	}
	var request sandboxRPCCreateRequest
	if err := decodeSandboxRPCJSON(`{"workspaceId":"public-preset"}`, &request); err != nil || request.WorkspaceID != "public-preset" {
		t.Fatalf("existing workspaceId input changed: %+v, %v", request, err)
	}
}
