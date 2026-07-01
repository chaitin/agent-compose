package images

import (
	"context"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
)

func TestEnsureDriverImageSkipsNonDockerDrivers(t *testing.T) {
	testEnsureDriverImageSkipsNonDockerDrivers(t)
}

func TestIntegrationEnsureDriverImageSkipsNonDockerDrivers(t *testing.T) {
	testEnsureDriverImageSkipsNonDockerDrivers(t)
}

func TestE2EEnsureDriverImageSkipsNonDockerDrivers(t *testing.T) {
	testEnsureDriverImageSkipsNonDockerDrivers(t)
}

func testEnsureDriverImageSkipsNonDockerDrivers(t *testing.T) {
	t.Helper()
	backend := &fakeImageBackend{
		inspectImage: func(context.Context, ImageInspectRequest) (ImageInspectResult, error) {
			t.Fatal("non-Docker driver should not inspect Docker images")
			return ImageInspectResult{}, nil
		},
	}
	for _, driver := range []string{driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		if err := EnsureDriverImage(context.Background(), &appconfig.Config{}, backend, DriverImageEnsureRequest{
			Driver:      driver,
			ImageRef:    "guest:v1",
			ProjectName: "skip",
			AgentName:   driver,
		}); err != nil {
			t.Fatalf("EnsureDriverImage(%s) returned error: %v", driver, err)
		}
	}
}
