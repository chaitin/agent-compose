package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type composeEnvContractFile struct {
	Services map[string]composeEnvContractService `yaml:"services"`
}

type composeEnvContractService struct {
	Image         string                       `yaml:"image"`
	Environment   map[string]string            `yaml:"environment"`
	Volumes       []string                     `yaml:"volumes"`
	WorkingDir    string                       `yaml:"working_dir"`
	Privileged    *bool                        `yaml:"privileged"`
	Devices       []string                     `yaml:"devices"`
	Ports         []string                     `yaml:"ports"`
	Command       []string                     `yaml:"command"`
	NetworkMode   string                       `yaml:"network_mode"`
	ContainerName string                       `yaml:"container_name"`
	Networks      map[string]composeEnvNetwork `yaml:"networks"`
}

type composeEnvNetwork struct {
	Aliases []string `yaml:"aliases"`
}

func TestE2EDockerComposeSandboxEnvContract(t *testing.T) {
	root := repoRootForComposeEnvTest(t)
	composeData := readRepoFileForComposeEnvTest(t, root, "docker-compose.yml")
	envExample := string(readRepoFileForComposeEnvTest(t, root, ".env.example"))
	dockerfile := string(readRepoFileForComposeEnvTest(t, root, "Dockerfile"))
	localDockerfile := string(readRepoFileForComposeEnvTest(t, root, "Dockerfile.agent-compose-local"))

	var composeFile composeEnvContractFile
	if err := yaml.Unmarshal(composeData, &composeFile); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	service, ok := composeFile.Services["agent-compose"]
	if !ok {
		t.Fatalf("docker-compose.yml missing agent-compose service")
	}
	if !strings.Contains(service.Image, "AGENT_COMPOSE_IMAGE") || !strings.Contains(service.Image, "chaitin/agent-compose:latest") {
		t.Fatalf("agent-compose image should stay deployable from published image, got %q", service.Image)
	}
	if hostRoot, ok := service.Environment["DOCKER_HOST_SANDBOX_ROOT"]; ok {
		t.Fatalf("compose must leave DOCKER_HOST_SANDBOX_ROOT unset for mount auto-detection, got %q", hostRoot)
	}
	for key := range service.Environment {
		if strings.Contains(key, "SESSION_ROOT") {
			t.Fatalf("compose service still exposes legacy session root env %q", key)
		}
	}
	for _, volume := range []string{"/var/run/docker.sock:/var/run/docker.sock", "/etc/localtime:/etc/localtime:ro", "${AGENT_COMPOSE_DATA_DIR:-./data}:/data", "./.env:/data/work/.env:ro"} {
		if !stringSliceContains(service.Volumes, volume) {
			t.Fatalf("agent-compose volumes = %#v, want %q", service.Volumes, volume)
		}
	}
	if service.WorkingDir != "/data/work" {
		t.Fatalf("agent-compose working_dir = %q, want /data/work", service.WorkingDir)
	}
	frontend, ok := composeFile.Services["agent-compose-ui"]
	if !ok {
		t.Fatalf("docker-compose.yml missing agent-compose-ui service")
	}
	if !stringSliceContains(frontend.Volumes, "${AGENT_COMPOSE_UI_DATA_DIR:-./data/agent-compose-ui}:/data") {
		t.Fatalf("agent-compose-ui volumes = %#v, want persistent UI data mount", frontend.Volumes)
	}
	if !stringSliceContains(frontend.Volumes, "${AGENT_COMPOSE_DATA_DIR:-./data}/sandboxes:/data/sandboxes:ro") {
		t.Fatalf("agent-compose-ui volumes = %#v, want read-only daemon sandbox mount", frontend.Volumes)
	}

	for _, want := range []string{
		"AGENT_COMPOSE_DATA_DIR=./data",
		"# SANDBOX_ROOT=/data/sandboxes",
		"# DOCKER_HOST_SANDBOX_ROOT=/absolute/host/path/to/data/sandboxes",
		"SESSION_ROOT, DOCKER_HOST_SESSION_ROOT, SESSION_START_TIMEOUT, and",
	} {
		if !strings.Contains(envExample, want) {
			t.Fatalf(".env.example missing %q", want)
		}
	}
	legacyAssignment := regexp.MustCompile(`(?m)^\s*(SESSION_ROOT|DOCKER_HOST_SESSION_ROOT|SESSION_START_TIMEOUT|SESSION_STOP_TIMEOUT)=`)
	if legacyAssignment.MatchString(envExample) {
		t.Fatalf(".env.example must not provide copyable legacy session env defaults")
	}
	for _, legacy := range []string{"SESSION_ROOT", "DOCKER_HOST_SESSION_ROOT"} {
		if strings.Contains(string(composeData), legacy) {
			t.Fatalf("docker-compose.yml contains legacy env %s", legacy)
		}
	}
	if strings.Contains(dockerfile, "ENV SANDBOX_ROOT=") {
		t.Fatalf("Dockerfile must leave SANDBOX_ROOT unset for legacy root detection")
	}
	if strings.Contains(dockerfile, "ENV SESSION_ROOT=") {
		t.Fatalf("Dockerfile still defines legacy SESSION_ROOT")
	}
	for name, content := range map[string]string{
		"Dockerfile":                     dockerfile,
		"Dockerfile.agent-compose-local": localDockerfile,
	} {
		for _, want := range []string{
			"ENV CAP_GRPC_LISTEN=0.0.0.0:7420",
			"ENV CAP_GRPC_TARGET=agent-compose:7420",
		} {
			if !strings.Contains(content, want) {
				t.Fatalf("%s missing image-level capability proxy default %q", name, want)
			}
		}
	}
	if strings.Contains(dockerfile, "/usr/share/zoneinfo/Asia/Shanghai") || strings.Contains(localDockerfile, "/usr/share/zoneinfo/Asia/Shanghai") {
		t.Fatal("daemon Dockerfiles must not hard-code the timezone")
	}
}

func TestDockerComposeKVMOverlayContract(t *testing.T) {
	root := repoRootForComposeEnvTest(t)
	baseData := readRepoFileForComposeEnvTest(t, root, "docker-compose.yml")
	overlayData := readRepoFileForComposeEnvTest(t, root, "docker-compose.kvm.yml")
	localOverride := string(readRepoFileForComposeEnvTest(t, root, "docker-compose.override.yml.example"))
	dockerfile := string(readRepoFileForComposeEnvTest(t, root, "Dockerfile"))

	var base composeEnvContractFile
	if err := yaml.Unmarshal(baseData, &base); err != nil {
		t.Fatalf("parse docker-compose.yml: %v", err)
	}
	baseService, ok := base.Services["agent-compose"]
	if !ok {
		t.Fatal("docker-compose.yml missing agent-compose service")
	}
	if baseService.Privileged != nil {
		t.Fatalf("base agent-compose privileged = %v, want omitted", *baseService.Privileged)
	}
	if len(baseService.Devices) != 0 {
		t.Fatalf("base agent-compose devices = %#v, want none", baseService.Devices)
	}
	baseServiceKeys := composeContractServiceKeys(t, baseData, "docker-compose.yml", "agent-compose")
	for _, forbidden := range []string{"build", "privileged", "devices"} {
		if _, ok := baseServiceKeys[forbidden]; ok {
			t.Fatalf("base agent-compose must omit %q", forbidden)
		}
	}
	if strings.Contains(string(baseData), "/dev/kvm") {
		t.Fatal("base Compose must not require /dev/kvm")
	}
	if _, ok := baseService.Environment["RUNTIME_DRIVER"]; ok {
		t.Fatal("base Compose must preserve the image-level RUNTIME_DRIVER default")
	}
	if !strings.Contains(dockerfile, "ENV RUNTIME_DRIVER=docker") {
		t.Fatal("Dockerfile must keep Docker as the image-level runtime default")
	}
	if !stringSliceContains(baseService.Ports, "127.0.0.1:7410:7410") {
		t.Fatalf("base agent-compose ports = %#v, want loopback daemon port", baseService.Ports)
	}

	var overlay composeEnvContractFile
	if err := yaml.Unmarshal(overlayData, &overlay); err != nil {
		t.Fatalf("parse docker-compose.kvm.yml: %v", err)
	}
	hostService, ok := overlay.Services["sandbox-host"]
	if !ok {
		t.Fatal("docker-compose.kvm.yml missing sandbox-host service")
	}
	if hostService.Privileged == nil || !*hostService.Privileged {
		t.Fatalf("sandbox-host privileged = %v, want true", hostService.Privileged)
	}
	if len(hostService.Devices) != 1 || hostService.Devices[0] != "/dev/kvm:/dev/kvm" {
		t.Fatalf("sandbox-host devices = %#v, want only /dev/kvm", hostService.Devices)
	}
	if !stringSliceContains(hostService.Volumes, "sandbox-host-run:/run/agent-compose") {
		t.Fatalf("sandbox-host volumes = %#v, want shared sandbox-host socket volume", hostService.Volumes)
	}
	if !stringSliceContains(hostService.Ports, "127.0.0.1:7410:7410") {
		t.Fatalf("sandbox-host ports = %#v, want loopback daemon port", hostService.Ports)
	}
	if network, ok := hostService.Networks["default"]; !ok || !stringSliceContains(network.Aliases, "agent-compose") {
		t.Fatalf("sandbox-host networks = %#v, want agent-compose alias", hostService.Networks)
	}

	daemonService, ok := overlay.Services["agent-compose"]
	if !ok {
		t.Fatal("docker-compose.kvm.yml missing agent-compose service")
	}
	if daemonService.Privileged != nil {
		t.Fatalf("overlay agent-compose privileged = %v, want omitted (moved to sandbox-host)", *daemonService.Privileged)
	}
	if len(daemonService.Devices) != 0 {
		t.Fatalf("overlay agent-compose devices = %#v, want none (moved to sandbox-host)", daemonService.Devices)
	}
	if daemonService.NetworkMode != "service:sandbox-host" {
		t.Fatalf("overlay agent-compose network_mode = %q, want service:sandbox-host", daemonService.NetworkMode)
	}
	if !stringSliceContains(daemonService.Volumes, "sandbox-host-run:/run/agent-compose") {
		t.Fatalf("overlay agent-compose volumes = %#v, want shared sandbox-host socket volume", daemonService.Volumes)
	}

	if !strings.Contains(localOverride, "build:") {
		t.Fatal("local Compose override example must retain build behavior")
	}
	for _, forbidden := range []string{"privileged:", "devices:", "/dev/kvm", "COMPOSE_FILE"} {
		if strings.Contains(localOverride, forbidden) {
			t.Fatalf("local Compose override example contains deployment-only KVM setting %q", forbidden)
		}
	}
	assertComposeKVMOverlayKeys(t, overlayData)
}

func assertComposeKVMOverlayKeys(t *testing.T, data []byte) {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse docker-compose.kvm.yml structure: %v", err)
	}
	if len(document) != 2 {
		t.Fatalf("KVM overlay top-level keys = %#v, want services and volumes", document)
	}
	volumes, ok := document["volumes"].(map[string]any)
	if !ok {
		t.Fatalf("KVM overlay volumes = %#v, want sandbox-host-run", document["volumes"])
	}
	if _, ok := volumes["sandbox-host-run"]; !ok {
		t.Fatalf("KVM overlay volumes = %#v, want sandbox-host-run", document["volumes"])
	}
	services, ok := document["services"].(map[string]any)
	if !ok || len(services) != 2 {
		t.Fatalf("KVM overlay services = %#v, want sandbox-host and agent-compose", document["services"])
	}
	hostKeys := composeContractServiceKeys(t, data, "docker-compose.kvm.yml", "sandbox-host")
	for _, key := range []string{"command", "privileged", "devices", "volumes", "ports", "networks"} {
		if _, ok := hostKeys[key]; !ok {
			t.Fatalf("KVM overlay sandbox-host missing %q", key)
		}
	}
	daemonKeys := composeContractServiceKeys(t, data, "docker-compose.kvm.yml", "agent-compose")
	if _, ok := daemonKeys["privileged"]; ok {
		t.Fatalf("KVM overlay agent-compose must omit privileged (moved to sandbox-host)")
	}
	if _, ok := daemonKeys["devices"]; ok {
		t.Fatalf("KVM overlay agent-compose must omit devices (moved to sandbox-host)")
	}
	for _, key := range []string{"network_mode", "volumes"} {
		if _, ok := daemonKeys[key]; !ok {
			t.Fatalf("KVM overlay agent-compose missing %q", key)
		}
	}
}

func composeContractServiceKeys(t *testing.T, data []byte, fileName, serviceName string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("parse %s structure: %v", fileName, err)
	}
	services, ok := document["services"].(map[string]any)
	if !ok {
		t.Fatalf("%s services = %#v, want mapping", fileName, document["services"])
	}
	service, ok := services[serviceName].(map[string]any)
	if !ok {
		t.Fatalf("%s %s = %#v, want mapping", fileName, serviceName, services[serviceName])
	}
	return service
}

func repoRootForComposeEnvTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			t.Fatalf("could not locate repo root from test working directory")
		}
		wd = parent
	}
}

func readRepoFileForComposeEnvTest(t *testing.T, root, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}
