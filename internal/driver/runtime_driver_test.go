package driver

import (
	appconfig "agent-compose/internal/config"
	"testing"
)

func TestResolveRuntimeDriverDocker(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{name: "docker", input: "docker", want: RuntimeDriverDocker},
		{name: "docker-engine alias", input: "docker-engine", want: RuntimeDriverDocker},
		{name: "microsandbox alias", input: "msb", want: RuntimeDriverMicrosandbox},
		{name: "default", input: "", want: RuntimeDriverDocker},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveRuntimeDriver(tc.input); got != tc.want {
				t.Fatalf("ResolveRuntimeDriver(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestValidateRuntimeDriverDocker(t *testing.T) {
	if err := ValidateRuntimeDriver(RuntimeDriverDocker); err != nil {
		t.Fatalf("ValidateRuntimeDriver(%q) returned error: %v", RuntimeDriverDocker, err)
	}
}

func TestDriverDefaultsForDocker(t *testing.T) {
	config := &appconfig.Config{
		DefaultImage:       "box-image:latest",
		DockerDefaultImage: "docker-image:latest",
		BoxliteHome:        "/tmp/boxlite",
		DockerHome:         "/tmp/docker",
		MicrosandboxHome:   "/tmp/microsandbox",
	}

	if got := DefaultGuestImageForDriver(config, RuntimeDriverDocker); got != config.DockerDefaultImage {
		t.Fatalf("DefaultGuestImageForDriver(docker) = %q, want %q", got, config.DockerDefaultImage)
	}
	if got := RuntimeHomeForDriver(config, RuntimeDriverDocker); got != config.DockerHome {
		t.Fatalf("RuntimeHomeForDriver(docker) = %q, want %q", got, config.DockerHome)
	}
}
