package webhooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/events/webhooks"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/storage/sqlite"
	"github.com/labstack/echo/v4"
)

func TestIntegrationEventTraceDuringSandboxIndexRepair(t *testing.T) {
	root := t.TempDir()
	database, err := sqlite.Open(filepath.Join(root, "data.db"), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Error(err)
		}
	})
	started, release := make(chan struct{}), make(chan struct{})
	var failing atomic.Bool
	var attempts atomic.Int32
	resolver := traceRepairProjectResolver(func(ctx context.Context, _ []*domain.Sandbox) (map[string]string, error) {
		if !failing.Load() {
			return map[string]string{}, nil
		}
		if attempts.Add(1) == 1 {
			return nil, context.DeadlineExceeded
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[string]string{}, nil
	})
	store, err := sandboxstore.NewWithDatabase(&appconfig.Config{SandboxRoot: root}, database.DB(), resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "traced-sandbox", Title: "original", Driver: "docker", CreatedAt: time.Unix(100, 0)}}
	if err := os.MkdirAll(filepath.Join(root, sandbox.Summary.ID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSandbox(sandbox); err != nil {
		t.Fatal(err)
	}
	events := configstore.FromDB(database.DB())
	event, err := events.CreateEvent(t.Context(), domain.TopicEventRecord{
		ID: "trace-event", Topic: "webhook.test.created", Source: domain.TopicEventSourceWebhook,
		PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := events.AddEventSandboxLink(t.Context(), domain.EventSandboxLink{
		EventID: event.ID, SandboxID: sandbox.Summary.ID, Relation: "scheduler.created",
	}); err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	sandbox.Summary.Title = "fresh trace title"
	if err := store.SaveSandbox(sandbox); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("repair did not start")
	}
	defer close(release)

	app := echo.New()
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{Store: events, QueryStore: events, Sandboxes: store})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/events/"+event.ID+"/trace", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("trace status = %d, body = %s", response.Code, response.Body.String())
	}
	var trace webhooks.EventTraceResponse
	if err := json.Unmarshal(response.Body.Bytes(), &trace); err != nil {
		t.Fatal(err)
	}
	if trace.Event.EventID != event.ID || len(trace.Sandboxes) != 1 || trace.Sandboxes[0].Sandbox == nil {
		t.Fatalf("trace = %#v", trace)
	}
	if got := trace.Sandboxes[0].Sandbox.Title; got != sandbox.Summary.Title || trace.SandboxSummariesIncomplete {
		t.Fatalf("title = %q, incomplete = %v", got, trace.SandboxSummariesIncomplete)
	}
}

type traceRepairProjectResolver func(context.Context, []*domain.Sandbox) (map[string]string, error)

func (f traceRepairProjectResolver) ResolveSandboxProjectIDs(ctx context.Context, sandboxes []*domain.Sandbox) (map[string]string, error) {
	return f(ctx, sandboxes)
}
