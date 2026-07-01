package execution

import (
	"context"
	"time"
)

func ExecContext(ctx context.Context, timeoutMs uint32) (context.Context, context.CancelFunc) {
	if timeoutMs == 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}
