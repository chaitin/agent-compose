package loaders

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestQJSLoaderEnginePreservesCallbackObjectResult(t *testing.T) {
	result, err := (&QJSLoaderEngine{}).Execute(context.Background(), LoaderExecutionRequest{
		Runtime:     domain.LoaderRuntimeScheduler,
		PayloadJSON: `{"request":"gamma-invoke"}`,
		Script: `
scheduler.interval("gamma-only", function gammaOnly(payload) {
	  return { entry: "gamma-only-callback", payload: payload || null };
}, 86400000);`,
	}, &coverageEngineHost{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	const want = `{"entry":"gamma-only-callback","payload":{"request":"gamma-invoke"}}`
	if result.ResultJSON != want {
		t.Fatalf("ResultJSON = %q, want %q", result.ResultJSON, want)
	}
}

func TestQJSLoaderEngineUsesCapturedJSONStringifier(t *testing.T) {
	result, err := (&QJSLoaderEngine{}).Execute(context.Background(), LoaderExecutionRequest{
		Runtime: domain.LoaderRuntimeScheduler,
		Script: `
JSON.stringify = function () { return "tampered"; };
function main() { return { source: "engine" }; }`,
	}, &coverageEngineHost{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ResultJSON != `{"source":"engine"}` {
		t.Fatalf("ResultJSON = %q, want trusted JSON serialization", result.ResultJSON)
	}
}

func TestQJSLoaderEngineReportsUnserializableResult(t *testing.T) {
	_, err := (&QJSLoaderEngine{}).Execute(context.Background(), LoaderExecutionRequest{
		Runtime: domain.LoaderRuntimeScheduler,
		Script: `
function main() {
  const result = {};
  result.self = result;
  return result;
}`,
	}, &coverageEngineHost{})
	if err == nil || !strings.Contains(err.Error(), "stringify js value") {
		t.Fatalf("Execute error = %v, want JSON serialization error", err)
	}
}

func TestQJSLoaderEngineAcceptsNullLogPayload(t *testing.T) {
	host := &capturingLogHost{}
	result, err := (&QJSLoaderEngine{}).Execute(context.Background(), LoaderExecutionRequest{
		Runtime: domain.LoaderRuntimeScheduler,
		Script: `function main() {
  scheduler.log("null payload", null);
  return { logged: true };
}`,
	}, host)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.ResultJSON != `{"logged":true}` {
		t.Fatalf("ResultJSON = %q", result.ResultJSON)
	}
	if host.message != "null payload" || host.payload != nil {
		t.Fatalf("Log message/payload = %q / %#v", host.message, host.payload)
	}
}

func TestQJSLoaderEngineCancellationInterruptsPendingPromise(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	host := &engineCancellationHost{started: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, err := (&QJSLoaderEngine{}).Execute(ctx, LoaderExecutionRequest{
			Runtime: domain.LoaderRuntimeScheduler,
			Script: `
scheduler.interval("pending", async function pending() {
  scheduler.log("callback started");
  await new Promise(function neverResolve() {});
}, 86400000);`,
			Trigger: &domain.LoaderTrigger{ID: "pending"},
		}, host)
		result <- err
	}()

	select {
	case <-host.started:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler callback did not start")
	}

	stopCause := errors.New("stop requested")
	cancel(stopCause)
	select {
	case err := <-result:
		if !errors.Is(err, stopCause) {
			t.Fatalf("Execute error = %v, want cancellation cause %v", err, stopCause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute did not return after its context was canceled")
	}
}

func TestQJSLoaderEnginePreservesSuccessfulResultWhenContextIsCanceledAfterExecution(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	stopCause := errors.New("late stop request")
	want := LoaderExecutionResult{ResultJSON: `{"ok":true}`, Warnings: []string{"preserved"}}

	result, err := executeWithQJSPanicRecovery(ctx, func() (LoaderExecutionResult, error) {
		cancel(stopCause)
		return want, nil
	})
	if err != nil {
		t.Fatalf("executeWithQJSPanicRecovery returned error: %v", err)
	}
	if result.ResultJSON != want.ResultJSON || len(result.Warnings) != 1 || result.Warnings[0] != "preserved" {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
}

type engineCancellationHost struct {
	coverageEngineHost
	started chan struct{}
	once    sync.Once
}

type capturingLogHost struct {
	coverageEngineHost
	message string
	payload any
}

func (h *capturingLogHost) Log(_ context.Context, message string, payload any) error {
	h.message = message
	h.payload = payload
	return nil
}

func (h *engineCancellationHost) Log(context.Context, string, any) error {
	h.once.Do(func() { close(h.started) })
	return nil
}
