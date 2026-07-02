package dockerdriver

import (
	"context"
	"time"

	"github.com/docker/docker/client"
)

const dockerProbeTimeout = 750 * time.Millisecond

func dockerDaemonAvailable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, dockerProbeTimeout)
	defer cancel()
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return false
	}
	defer func() { _ = dockerClient.Close() }()
	_, err = dockerClient.Ping(probeCtx)
	return err == nil
}
