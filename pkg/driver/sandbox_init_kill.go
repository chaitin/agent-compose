package driver

import (
	"context"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
)

// killSandboxInitTree terminates every process tagged with marker, including
// descendants that moved into their own process group. Exec session leaders
// are killed separately because a detached workload can outlive its group.
func killSandboxInitTree(client *sandboxinit.Client, marker string) {
	if client == nil || strings.TrimSpace(marker) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = client.KillTree(ctx, marker)
}
