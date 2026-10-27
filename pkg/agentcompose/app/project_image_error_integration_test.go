package app

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/apierror"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/imagecache"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/volumes"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/errdetail"
)

// missingImages reports the driver image as absent locally and unpullable.
type missingImages struct{}

func (missingImages) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (missingImages) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, images.OpError{
		Op:       "inspect image",
		ImageRef: "agent-compose-guest:test",
		Err:      imagecache.NewError(imagecache.ErrorKindNotFound, "inspect", "agent-compose-guest:test", errors.New("missing")),
	}
}

func (missingImages) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, images.OpError{
		Op:       "pull image",
		ImageRef: "agent-compose-guest:test",
		Err:      imagecache.NewError(imagecache.ErrorKindNotFound, "pull", "agent-compose-guest:test", errors.New("missing")),
	}
}

func (missingImages) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

// TestApplyProjectMissingImageReportsNotApplied is the acceptance example of
// issue #812: a dependency failure is a determined failure with no side effect,
// not an unclassified "unavailable".
func TestApplyProjectMissingImageReportsNotApplied(t *testing.T) {
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
		Images:  missingImages{},
		Volumes: volumes.NewManager(store),
	})
	handler := api.NewProjectHandler(projectControllerDelegate{controller: controller, config: config}, store, nil, nil)
	_, connectHandler := agentcomposev2connect.NewProjectServiceHandler(handler)
	server := httptest.NewServer(connectHandler)
	t.Cleanup(server.Close)
	client := agentcomposev2connect.NewProjectServiceClient(server.Client(), server.URL)

	_, err = client.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "missing-image",
			Agents: []*agentcomposev2.AgentSpec{{
				Name:     "worker",
				Provider: "codex",
				Image:    "agent-compose-guest:test",
			}},
		},
		Source: &agentcomposev2.ProjectSource{ComposePath: "/srv/project/compose.yaml"},
	}))
	if err == nil {
		t.Fatal("ApplyProject accepted a missing image")
	}
	if code := connect.CodeOf(err); code != connect.CodeUnavailable {
		t.Fatalf("ApplyProject code = %s, want unavailable (error: %v)", code, err)
	}
	detail, ok := errdetail.From(err)
	if !ok {
		t.Fatalf("ApplyProject error carries no detail: %v", err)
	}
	if detail.Reason != api.ReasonToProto(apierror.ReasonImageNotFound) {
		t.Fatalf("reason = %s, want image_not_found", detail.Reason)
	}
	if detail.Effect != api.EffectToProto(apierror.EffectNotApplied) {
		t.Fatalf("effect = %s, want not_applied", detail.Effect)
	}
}
