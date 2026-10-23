package app

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/egress"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// TestIntegrationRejectedNetworkPolicyLeavesNoProject pins the admission
// ordering: a candidate the daemon-wide policy refuses must be rejected before
// the apply writes a project or revision. Without the pre-write admission the
// daemon persists the rejected revision, reports InvalidArgument, and leaves a
// project the caller cannot run.
func TestIntegrationRejectedNetworkPolicyLeavesNoProject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:  root,
		DbAddr:    filepath.Join(root, "data.db"),
		DbTimeout: 5 * time.Second,
		GlobalNetworkPolicy: &egress.NetworkDeclaration{
			Default: egress.Deny,
			Allow: []egress.AllowEntry{{
				Host:     "10.2.96.126",
				Port:     19180,
				Protocol: egress.ProtocolAny,
			}},
		},
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
		Admit:   networkPolicyAdmission(nil, config),
	})
	handler := api.NewProjectHandler(projectControllerDelegate{controller: controller, config: config}, store, nil, nil)
	_, connectHandler := agentcomposev2connect.NewProjectServiceHandler(handler)
	server := httptest.NewServer(connectHandler)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)

	// The agent allows port 19181, which the daemon-wide allowlist does not cover.
	_, err = client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "rejected-policy",
			Agents: []*agentcomposev2.AgentSpec{{
				Name:     "worker",
				Provider: "codex",
				Image:    "agent-compose-guest:test",
				Sandbox: &agentcomposev2.SandboxSpec{Network: &agentcomposev2.SandboxNetworkSpec{
					Default: "deny-all",
					Allow:   []*agentcomposev2.SandboxNetworkAllowSpec{{Host: "10.2.96.126", Port: 19181}},
				}},
			}},
		},
		Source: &agentcomposev2.ProjectSource{ComposePath: "/srv/project/compose.yaml"},
	}))
	if err == nil {
		t.Fatal("ApplyProject accepted an allowance outside the daemon network policy")
	}
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("ApplyProject error code = %v, want %v (error: %v)", code, connect.CodeInvalidArgument, err)
	}
	if !strings.Contains(err.Error(), "outside the daemon network policy") {
		t.Fatalf("ApplyProject error = %v, want the layering message", err)
	}

	list, err := store.ListProjects(ctx, domain.ProjectListOptions{})
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(list.Projects) != 0 {
		t.Fatalf("rejected apply persisted %d project(s): %+v", len(list.Projects), list.Projects)
	}
}

// presentImages keeps the apply from stopping at image resolution, so the test
// exercises the admission/persistence ordering rather than the image backend.
type presentImages struct{}

func (presentImages) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (presentImages) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, nil
}

func (presentImages) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, nil
}

func (presentImages) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}
