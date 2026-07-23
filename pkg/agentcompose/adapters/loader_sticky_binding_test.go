package adapters

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestLoaderRequestSandboxConfigHashIgnoresVolumeMountOrder(t *testing.T) {
	mounts := []domain.SandboxVolumeMount{
		{ID: "volume-data", Type: domain.VolumeMountTypeVolume, Source: "data", Target: "/workspace/data", HostPath: "/volumes/data", VolumeID: "volume-1"},
		{ID: "bind-cache", Type: domain.VolumeMountTypeBind, Source: "./cache", Target: "/workspace/cache", HostPath: "/project/cache", ProjectPath: "/project"},
	}
	first, err := loaderRequestSandboxConfigHash("sha256:loader", domain.LoaderAgentRequest{}, nil, nil, nil, nil, "docker", "guest:v1", mounts)
	if err != nil {
		t.Fatalf("loaderRequestSandboxConfigHash returned error: %v", err)
	}
	reordered := []domain.SandboxVolumeMount{mounts[1], mounts[0]}
	second, err := loaderRequestSandboxConfigHash("sha256:loader", domain.LoaderAgentRequest{}, nil, nil, nil, nil, "docker", "guest:v1", reordered)
	if err != nil {
		t.Fatalf("loaderRequestSandboxConfigHash reordered returned error: %v", err)
	}
	if second != first {
		t.Fatalf("volume mount ordering changed loader sandbox hash: got %q want %q", second, first)
	}
}
