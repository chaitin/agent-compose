package dockerprobe

import (
	"context"
	"time"

	"github.com/docker/docker/client"
)

func Available(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return false
	}
	defer func() { _ = dockerClient.Close() }()

	_, err = dockerClient.Ping(probeCtx)
	return err == nil
}
