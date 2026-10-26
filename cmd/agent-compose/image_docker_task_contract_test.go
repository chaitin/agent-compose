package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImageDockerTaskContract(t *testing.T) {
	root := repoRootForImageDockerTaskTest(t)
	taskfile := readImageDockerTaskContractFile(t, root, "Taskfile.yml")
	testScript := readImageDockerTaskContractFile(t, root, "scripts/test-image-docker-e2e.sh")
	testingDoc := readImageDockerTaskContractFile(t, root, "TESTING.md")
	for _, want := range []string{"test:e2e:image-docker:", "./scripts/test-image-docker-e2e.sh"} {
		if !strings.Contains(taskfile, want) {
			t.Fatalf("Taskfile.yml missing image Docker E2E contract %q", want)
		}
	}
	for _, want := range []string{
		"AGENT_COMPOSE_E2E_DAEMON_IMAGE",
		"AGENT_COMPOSE_E2E_GUEST_IMAGE",
		"AGENT_COMPOSE_E2E_DOCKER_SOCKET",
		"AGENT_COMPOSE_E2E_RUN_ID",
		"TestE2EImageDocker(NoKVMStartup|SandboxLifecycle|SandboxNetworkPlacement)",
		"label=agent-compose.e2e=image-docker",
		"cleanup_image_docker_e2e_resources",
	} {
		if !strings.Contains(testScript, want) {
			t.Fatalf("test-image-docker-e2e.sh missing contract %q", want)
		}
	}
	for _, want := range []string{"task test:e2e:image-docker", "no Docker socket, privilege, device, or `/dev/kvm`", "create, exec, stop, resume, exec, and remove"} {
		if !strings.Contains(testingDoc, want) {
			t.Fatalf("TESTING.md missing image Docker E2E contract %q", want)
		}
	}
}

func repoRootForImageDockerTaskTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("find repository root from %s", dir)
		}
		dir = parent
	}
}

func readImageDockerTaskContractFile(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}
