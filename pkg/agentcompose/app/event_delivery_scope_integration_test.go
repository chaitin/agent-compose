package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sqlite"
)

// TestIntegrationEventDeliveryScope reproduces issue #722: Project A publishes
// workflow.x.ready from a scheduler run while subscribers in Project A and
// Project B both listen for it. The event travels the production path: the
// runtime host persists it, the event dispatcher reads it back from the store
// and hands it to the scheduler bus, and the controller matches subscribers.
func TestIntegrationEventDeliveryScope(t *testing.T) {
	tests := []struct {
		name  string
		scope domain.EventDeliveryScope
		want  []string
	}{
		{name: "project scope keeps the event in the publisher's project", scope: domain.EventDeliveryScopeProject, want: []string{"scope-a-sub"}},
		{name: "daemon scope delivers to every project", scope: domain.EventDeliveryScopeDaemon, want: []string{"scope-a-sub", "scope-b-sub"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			root := t.TempDir()
			database, err := sqlite.Open(filepath.Join(root, "data.db"), time.Second)
			if err != nil {
				t.Fatalf("open database: %v", err)
			}
			t.Cleanup(func() {
				if err := database.Close(); err != nil {
					t.Errorf("close database: %v", err)
				}
			})
			store := configstore.FromDB(database.DB())
			subscriberA := seedWebhookStopSchedulerNamed(t, ctx, store, "scope-project-a", "scope-agent-a", "scope-a-sub", "workflow.x.ready")
			subscriberB := seedWebhookStopSchedulerNamed(t, ctx, store, "scope-project-b", "scope-agent-b", "scope-b-sub", "workflow.x.ready")

			engine := &recordingSchedulerEngine{ran: make(chan string, 4)}
			bus := schedulers.NewBusWithBuffer(4)
			controller := schedulers.NewController(schedulers.ControllerDependencies{
				RootCtx:            ctx,
				Store:              store,
				Engine:             engine,
				Publisher:          bus,
				EventDeliveryScope: tt.scope,
				HostFactory: func(scheduler domain.Scheduler, execution schedulers.RuntimeExecutionContext, triggerEvent schedulers.TriggerEventMetadata) schedulers.RunHost {
					return schedulers.NewRuntimeHost(schedulers.RunHostDependencies{}, scheduler, execution, triggerEvent)
				},
				Artifacts: schedulers.FSArtifacts{DataRoot: root},
				Schedulers: map[string]domain.Scheduler{
					subscriberA.Summary.ID: subscriberA,
					subscriberB.Summary.ID: subscriberB,
				},
				RunTimeout: func(time.Duration) time.Duration { return time.Minute },
			})

			publisher := schedulers.NewRuntimeHost(schedulers.RunHostDependencies{Store: store}, subscriberA,
				schedulers.RuntimeExecutionContext{ID: "publisher-run", Kind: schedulers.ExecutionKindTrigger}, schedulers.TriggerEventMetadata{})
			published, err := publisher.PublishEvent(ctx, "workflow.x.ready", `{"marker":"abc"}`)
			if err != nil {
				t.Fatalf("publish event: %v", err)
			}
			events.NewDispatcher(ctx, store, bus).DispatchOnce(ctx, 10)
			select {
			case event := <-bus.Events():
				controller.DispatchEvent(event)
			case <-time.After(5 * time.Second):
				t.Fatal("dispatcher did not hand the stored event to the scheduler bus")
			}

			ran := make([]string, 0, len(tt.want))
			for range tt.want {
				select {
				case schedulerID := <-engine.ran:
					ran = append(ran, schedulerID)
				case <-time.After(5 * time.Second):
					t.Fatalf("subscriber runs = %v, want %v", ran, tt.want)
				}
			}
			slices.Sort(ran)
			if !slices.Equal(ran, tt.want) {
				t.Fatalf("subscriber runs = %v, want %v", ran, tt.want)
			}
			if payload := engine.payloadFor("scope-a-sub"); !strings.Contains(payload, `"marker":"abc"`) {
				t.Fatalf("publisher's own subscriber payload = %s, want the marker", payload)
			}

			// Deliveries are recorded before any run starts, so they show every
			// subscriber the event matched, including one whose run is still
			// pending when the assertion runs.
			deliveries, err := store.ListEventDeliveries(ctx, []string{published.ID})
			if err != nil {
				t.Fatalf("list event deliveries: %v", err)
			}
			delivered := make([]string, 0, len(deliveries))
			for _, delivery := range deliveries {
				delivered = append(delivered, delivery.SchedulerID)
			}
			slices.Sort(delivered)
			if !slices.Equal(delivered, tt.want) {
				t.Fatalf("event deliveries = %v, want %v", delivered, tt.want)
			}
		})
	}
}

// recordingSchedulerEngine completes every run immediately and records which
// scheduler's trigger ran with which payload.
type recordingSchedulerEngine struct {
	ran      chan string
	mu       sync.Mutex
	payloads map[string]string
}

func (*recordingSchedulerEngine) Validate(context.Context, string, string) (schedulers.SchedulerValidationResult, error) {
	return schedulers.SchedulerValidationResult{}, nil
}

func (e *recordingSchedulerEngine) Execute(_ context.Context, req schedulers.SchedulerExecutionRequest, _ schedulers.SchedulerHost) (schedulers.SchedulerExecutionResult, error) {
	schedulerID := ""
	if req.Trigger != nil {
		schedulerID = strings.TrimSuffix(req.Trigger.ID, "-trigger")
	}
	e.mu.Lock()
	if e.payloads == nil {
		e.payloads = map[string]string{}
	}
	e.payloads[schedulerID] = req.PayloadJSON
	e.mu.Unlock()
	e.ran <- schedulerID
	return schedulers.SchedulerExecutionResult{ResultJSON: "null"}, nil
}

func (e *recordingSchedulerEngine) payloadFor(schedulerID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.payloads[schedulerID]
}
