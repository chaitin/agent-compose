package app

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/internal/projects"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestIntegrationApplyProjectRecordsApplierTrustedHeaders(t *testing.T) {
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
	delegate := projectControllerDelegate{controller: projects.NewController(projects.ControllerDependencies{
		Config: config, Store: store, Volumes: volumes.NewManager(store),
	})}
	apply := func(ctx context.Context, dryRun bool) string {
		t.Helper()
		response, err := delegate.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
			Spec:   &agentcomposev2.ProjectSpec{Name: "apply-identity"},
			Source: &agentcomposev2.ProjectSource{ComposePath: "/srv/project/compose.yaml"},
			DryRun: dryRun,
		}))
		if err != nil {
			t.Fatalf("ApplyProject: %v", err)
		}
		return response.Msg.GetProject().GetSummary().GetProjectId()
	}
	storedHeaders := func(projectID string) []domain.TrustedHeader {
		t.Helper()
		headers, err := store.ProjectApplyTrustedHeaders(ctx, projectID)
		if err != nil {
			t.Fatalf("ProjectApplyTrustedHeaders: %v", err)
		}
		return headers
	}

	alice := []domain.TrustedHeader{
		{Name: "x-mpi-role", Value: "admin"},
		{Name: "x-mpi-user-id", Value: "alice"},
	}
	projectID := apply(domain.NewContextWithTrustedHeaders(ctx, alice), false)
	if got := storedHeaders(projectID); !reflect.DeepEqual(got, alice) {
		t.Fatalf("headers after first apply = %#v, want %#v", got, alice)
	}

	bob := []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "bob"}}
	apply(domain.NewContextWithTrustedHeaders(ctx, bob), true)
	if got := storedHeaders(projectID); !reflect.DeepEqual(got, alice) {
		t.Fatalf("dry run replaced recorded headers: %#v", got)
	}

	// An unchanged spec applied by someone else still changes who runs as.
	apply(domain.NewContextWithTrustedHeaders(ctx, bob), false)
	if got := storedHeaders(projectID); !reflect.DeepEqual(got, bob) {
		t.Fatalf("headers after reapply = %#v, want %#v", got, bob)
	}

	apply(ctx, false)
	if got := storedHeaders(projectID); got != nil {
		t.Fatalf("apply without trusted headers kept %#v", got)
	}
}
