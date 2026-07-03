package loaders

import (
	"context"
	"time"
)

func CommandContext(ctx context.Context, timeoutMs int64) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}
