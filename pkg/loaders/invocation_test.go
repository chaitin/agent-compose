package loaders

import (
	"context"
	"errors"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestInvocationExecutorUsesEphemeralContextAndSharedConcurrencyGate(t *testing.T) {
	engine := &invocationEngineFake{result: LoaderExecutionResult{ResultJSON: `{"ok":true}`, Warnings: []string{"warning"}}}
	host := &invocationHostFake{}
	entered := 0
	left := 0
	var execution RuntimeExecutionContext
	executor := NewInvocationExecutor(InvocationExecutorDependencies{
		Engine: engine,
		HostFactory: func(_ domain.Loader, current RuntimeExecutionContext, _ TriggerEventMetadata) RunHost {
			execution = current
			return host
		},
		EnterRun: func(domain.Loader) bool { entered++; return true },
		LeaveRun: func(string) { left++ },
		NewID:    func() string { return "invocation-correlation" },
	})
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Runtime: domain.LoaderRuntimeScheduler}, Script: "function main() {}"}
	result, err := executor.Invoke(context.Background(), loader, ` { "value" : true } `)
	if err != nil || result.ResultJSON != `{"ok":true}` || len(result.Warnings) != 1 {
		t.Fatalf("Invoke result=%#v err=%v", result, err)
	}
	if execution.ID != "invocation-correlation" || execution.TriggerID != "" || execution.Kind != ExecutionKindInvocation {
		t.Fatalf("execution context=%#v", execution)
	}
	if engine.request.PayloadJSON != `{"value":true}` || entered != 1 || left != 1 || host.cleanupCalls != 1 {
		t.Fatalf("request/gate/cleanup=%#v/%d/%d/%d", engine.request, entered, left, host.cleanupCalls)
	}
}

func TestInvocationExecutorBusyAndFailureDoNotCreateRunLifecycle(t *testing.T) {
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Runtime: domain.LoaderRuntimeScheduler}, Script: "function main() {}"}
	busy := NewInvocationExecutor(InvocationExecutorDependencies{
		Engine: &invocationEngineFake{}, HostFactory: func(domain.Loader, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return &invocationHostFake{}
		},
		EnterRun: func(domain.Loader) bool { return false },
	})
	if _, err := busy.Invoke(context.Background(), loader, `{}`); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("busy error=%v", err)
	}
	host := &invocationHostFake{}
	left := 0
	failed := NewInvocationExecutor(InvocationExecutorDependencies{
		Engine: &invocationEngineFake{err: errors.New("script failed")}, HostFactory: func(domain.Loader, RuntimeExecutionContext, TriggerEventMetadata) RunHost { return host },
		EnterRun: func(domain.Loader) bool { return true }, LeaveRun: func(string) { left++ },
	})
	if _, err := failed.Invoke(context.Background(), loader, `{}`); err == nil || err.Error() != "script failed" || left != 1 || host.cleanupCalls != 1 {
		t.Fatalf("failure err=%v left=%d cleanup=%d", err, left, host.cleanupCalls)
	}
}

func TestInvocationExecutorFallsBackWhenIDGeneratorReturnsEmpty(t *testing.T) {
	var execution RuntimeExecutionContext
	executor := NewInvocationExecutor(InvocationExecutorDependencies{
		Engine: &invocationEngineFake{},
		HostFactory: func(_ domain.Loader, current RuntimeExecutionContext, _ TriggerEventMetadata) RunHost {
			execution = current
			return &invocationHostFake{}
		},
		NewID: func() string { return " " },
	})
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Runtime: domain.LoaderRuntimeScheduler}, Script: "function main() {}"}
	if _, err := executor.Invoke(context.Background(), loader, `{}`); err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if execution.ID == "" {
		t.Fatal("invocation execution context has an empty correlation ID")
	}
}

func TestInvocationExecutorPreservesSuccessfulResultWhenContextIsCanceledAfterExecution(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	stopCause := errors.New("late stop request")
	engine := &invocationEngineFake{
		result: LoaderExecutionResult{ResultJSON: `{"ok":true}`, Warnings: []string{"preserved"}},
		afterExecute: func() {
			cancel(stopCause)
		},
	}
	executor := NewInvocationExecutor(InvocationExecutorDependencies{
		Engine: engine,
		HostFactory: func(domain.Loader, RuntimeExecutionContext, TriggerEventMetadata) RunHost {
			return &invocationHostFake{}
		},
	})
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-1", Runtime: domain.LoaderRuntimeScheduler}, Script: "function main() {}"}

	result, err := executor.Invoke(ctx, loader, `{}`)
	if err != nil {
		t.Fatalf("Invoke returned error: %v", err)
	}
	if result.ResultJSON != `{"ok":true}` || len(result.Warnings) != 1 || result.Warnings[0] != "preserved" {
		t.Fatalf("Invoke result = %#v", result)
	}
}

type invocationEngineFake struct {
	request      LoaderExecutionRequest
	result       LoaderExecutionResult
	err          error
	afterExecute func()
}

func (e *invocationEngineFake) Validate(context.Context, string, string) (LoaderValidationResult, error) {
	return LoaderValidationResult{}, nil
}

func (e *invocationEngineFake) Execute(_ context.Context, request LoaderExecutionRequest, _ LoaderHost) (LoaderExecutionResult, error) {
	e.request = request
	if e.afterExecute != nil {
		e.afterExecute()
	}
	return e.result, e.err
}

type invocationHostFake struct{ cleanupCalls int }

func (*invocationHostFake) Log(context.Context, string, any) error { return nil }
func (*invocationHostFake) PublishEvent(context.Context, string, string) (domain.TopicEventRecord, error) {
	return domain.TopicEventRecord{}, nil
}
func (*invocationHostFake) Agent(context.Context, string, domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	return domain.LoaderAgentResult{}, nil
}
func (*invocationHostFake) Command(context.Context, domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	return domain.LoaderCommandResult{}, nil
}
func (*invocationHostFake) LLM(context.Context, string, domain.LoaderLLMRequest) (domain.LoaderLLMResult, error) {
	return domain.LoaderLLMResult{}, nil
}
func (*invocationHostFake) StateGet(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (*invocationHostFake) StateSet(context.Context, string, string) error { return nil }
func (*invocationHostFake) StateDelete(context.Context, string) error      { return nil }
func (*invocationHostFake) CallSessionRPC(context.Context, string, string) (string, error) {
	return "", nil
}
func (h *invocationHostFake) CleanupCommandSessions(context.Context) { h.cleanupCalls++ }
