package driver

import (
	"context"
	"errors"
	"testing"
)

func TestExecTerminationResultErrorPreservesPrimaryAndClassifiesUnconfirmedCleanup(t *testing.T) {
	primary := context.Canceled
	cleanup := errors.New("kill failed")
	err := execTerminationResultError(RuntimeDriverDocker, "exec-1", primary, cleanup)
	if !errors.Is(err, primary) {
		t.Fatalf("error = %v, want primary cancellation", err)
	}
	if !errors.Is(err, cleanup) {
		t.Fatalf("error = %v, want cleanup cause", err)
	}
	if !errors.Is(err, ErrExecTerminationUnconfirmed) {
		t.Fatalf("error = %v, want ErrExecTerminationUnconfirmed", err)
	}
}

func TestExecTerminationResultErrorReturnsPrimaryAfterConfirmedCleanup(t *testing.T) {
	if err := execTerminationResultError(RuntimeDriverDocker, "exec-1", context.Canceled, nil); !errors.Is(err, context.Canceled) || errors.Is(err, ErrExecTerminationUnconfirmed) {
		t.Fatalf("error = %v, want only context cancellation", err)
	}
}
