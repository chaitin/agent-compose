package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// TestIntegrationApplyCarriesPolicyResources pins the path `ac up` takes: the
// client hashes its normalized spec, sends the proto form with that hash, and
// the daemon renders it back to YAML and normalizes it again. A declared
// policy.resources must survive that round trip, so the hashes agree, the
// stored agent spec carries the declaration, and the resolver the driver seam
// reads returns it. Dropping the block on the wire made the daemon refuse the
// apply with a submitted_spec_hash mismatch, or apply it without the limit.
func TestIntegrationApplyCarriesPolicyResources(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want compose.NormalizedSandboxResourcesSpec
	}{
		{
			name: "resources alone",
			yaml: `
name: resources-apply
agents:
  worker:
    provider: codex
    image: agent-compose-guest:test
    policy:
      resources:
        cpus: 1.5
        memory: 1536Mi
`,
			want: compose.NormalizedSandboxResourcesSpec{CPUMillis: 1500, MemoryMiB: 1536},
		},
		{
			name: "resources next to a network policy",
			yaml: `
name: resources-apply
agents:
  worker:
    provider: codex
    image: agent-compose-guest:test
    policy:
      network:
        default: deny-all
        allow:
          - host: api.github.com
            port: 443
      resources:
        memory: 2Gi
`,
			want: compose.NormalizedSandboxResourcesSpec{MemoryMiB: 2048},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, client := newResourcesApplyHarness(t)

			parsed, err := compose.Parse([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("parse compose: %v", err)
			}
			normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{})
			if err != nil {
				t.Fatalf("normalize compose: %v", err)
			}
			clientHash, err := normalized.Hash()
			if err != nil {
				t.Fatalf("hash compose: %v", err)
			}
			protoSpec, err := api.ProjectSpecToProtoChecked(normalized)
			if err != nil {
				t.Fatalf("serialize compose: %v", err)
			}

			applied, err := client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
				Spec:              protoSpec,
				Source:            &agentcomposev2.ProjectSource{ComposePath: "/srv/project/compose.yaml"},
				SubmittedSpecHash: clientHash,
			}))
			if err != nil {
				t.Fatalf("ApplyProject: %v", err)
			}
			if issues := applied.Msg.GetIssues(); len(issues) != 0 {
				t.Fatalf("ApplyProject issues = %v, want none", issues)
			}
			if !applied.Msg.GetApplied() {
				t.Fatalf("ApplyProject applied = false: %#v", applied.Msg)
			}
			if got := applied.Msg.GetRevision().GetSpecHash(); got != clientHash {
				t.Fatalf("daemon spec hash = %s, want the client hash %s", got, clientHash)
			}

			agents, err := store.ListProjectAgents(ctx, applied.Msg.GetProject().GetSummary().GetProjectId())
			if err != nil || len(agents) != 1 {
				t.Fatalf("ListProjectAgents = %d agents, %v; want one", len(agents), err)
			}
			resolver, err := runs.NewSandboxRunTargetResolver(store)
			if err != nil {
				t.Fatalf("NewSandboxRunTargetResolver: %v", err)
			}
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
				ID:   "sandbox-1",
				Tags: []domain.SandboxTag{{Name: domain.AgentSandboxTagID, Value: agents[0].ID}},
			}}
			got, err := resolver.ResolveResourceLimits(ctx, sandbox)
			if err != nil {
				t.Fatalf("ResolveResourceLimits: %v", err)
			}
			if got == nil || *got != tc.want {
				t.Fatalf("stored resources = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func newResourcesApplyHarness(t *testing.T) (resourcesApplyStore, agentcomposev2connect.ProjectServiceClient) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:  root,
		DbAddr:    filepath.Join(root, "data.db"),
		DbTimeout: 5 * time.Second,
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("open migrated config store: %v", err)
	}
	controller := projects.NewController(projects.ControllerDependencies{
		Config:  config,
		Store:   store,
		Images:  presentImages{},
		Volumes: volumes.NewManager(store),
	})
	handler := api.NewProjectHandler(projectControllerDelegate{controller: controller, config: config}, store, nil, nil)
	_, connectHandler := agentcomposev2connect.NewProjectServiceHandler(handler)
	server := httptest.NewServer(connectHandler)
	t.Cleanup(server.Close)
	return store, agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)
}

type resourcesApplyStore interface {
	runs.SandboxRunTargetStore
	ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error)
}
