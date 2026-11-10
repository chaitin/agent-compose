package app

import (
	"context"
	"errors"
	"testing"
)

func TestStartupRecoveryBudgetsDoNotShareCancellation(t *testing.T) {
	want := errors.New("runtime reconciliation failed")
	var first context.Context
	err := withStartupRecoveryBudget(context.Background(), func(ctx context.Context) error {
		first = ctx
		return want
	})
	if !errors.Is(err, want) || !errors.Is(first.Err(), context.Canceled) {
		t.Fatalf("first recovery error=%v context error=%v", err, first.Err())
	}
	err = withStartupRecoveryBudget(context.Background(), func(ctx context.Context) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("recovery has no timeout")
		}
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("next recovery inherited previous cancellation: %v", err)
	}
}

func TestStartupRecoveryBudgetRespectsDaemonCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := withStartupRecoveryBudget(ctx, func(recoveryCtx context.Context) error {
		return recoveryCtx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery ignored daemon cancellation: %v", err)
	}
}
