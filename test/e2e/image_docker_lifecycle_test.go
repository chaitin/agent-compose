package e2e

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	cerrdefs "github.com/containerd/errdefs"
	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	mountapi "github.com/docker/docker/api/types/mount"
	networkapi "github.com/docker/docker/api/types/network"
	volumeapi "github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const (
	imageDockerE2EDaemonImageEnv = "AGENT_COMPOSE_E2E_DAEMON_IMAGE"
	imageDockerE2EGuestImageEnv  = "AGENT_COMPOSE_E2E_GUEST_IMAGE"
	imageDockerE2ESocketEnv      = "AGENT_COMPOSE_E2E_DOCKER_SOCKET"
	imageDockerE2ERunIDEnv       = "AGENT_COMPOSE_E2E_RUN_ID"
	imageDockerE2ELabel          = "agent-compose.e2e"
	imageDockerE2ELabelValue     = "image-docker"
	imageDockerDaemonPort        = "7410/tcp"
)

type imageDockerVersionEnvelope struct {
	Err  json.RawMessage `json:"err"`
	Msg  string          `json:"msg"`
	Data struct {
		Version         string   `json:"version"`
		OS              string   `json:"os"`
		Arch            string   `json:"arch"`
		CompiledDrivers []string `json:"compiled_drivers"`
		Timestamp       float64  `json:"timestamp"`
		Timezone        string   `json:"timezone"`
		TimezoneOffset  *int     `json:"timezone_offset"`
	} `json:"data"`
}

type imageDockerBuildInfo struct {
	Version         string   `json:"version"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	CompiledDrivers []string `json:"compiled_drivers"`
}

type imageDockerTLSConfig struct {
	certPath  string
	keyPath   string
	caPEM     []byte
	authToken string
}

type imageDockerFixture struct {
	docker      *client.Client
	containerID string
	networkID   string
	networkName string
	volumeName  string
	sandboxID   string
	baseURL     string
	tls         imageDockerTLSConfig
}

func TestE2EImageDockerNoKVMStartup(t *testing.T) {
	daemonImage := strings.TrimSpace(os.Getenv(imageDockerE2EDaemonImageEnv))
	if daemonImage == "" {
		t.Skipf("set %s to run the daemon image startup smoke", imageDockerE2EDaemonImageEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dockerClient := newE2EDockerClient(t, ctx, daemonImage)
	t.Cleanup(func() { _ = dockerClient.Close() })
	imageInfo, _, err := dockerClient.ImageInspectWithRaw(ctx, daemonImage)
	if err != nil {
		t.Fatalf("inspect daemon image %q: %v", daemonImage, err)
	}
	buildInfo := runImageDockerVersionCommand(t, ctx, dockerClient, daemonImage)
	assertImageDockerBuildInfo(t, buildInfo, imageInfo.Os, imageInfo.Architecture)

	fixture := startImageDockerStartupDaemon(t, ctx, dockerClient, daemonImage)
	version := waitForImageDockerVersion(t, ctx, fixture)
	assertImageDockerVersion(t, fixture, version, imageInfo.Os, imageInfo.Architecture)
	assertImageDockerDefaultDriver(t, ctx, fixture)
	assertImageDockerDaemonUnprivileged(t, ctx, fixture)
	assertImageDockerStartupHasNoSocket(t, ctx, fixture)
	assertImageDockerNativeHomesUninitialized(t, ctx, fixture)

	fixture.cleanup(t)
	assertImageDockerFixtureRemoved(t, fixture)
}

func TestE2EImageDockerSandboxLifecycle(t *testing.T) {
	daemonImage := strings.TrimSpace(os.Getenv(imageDockerE2EDaemonImageEnv))
	if daemonImage == "" {
		t.Skipf("set %s to run the containerized daemon Docker lifecycle smoke", imageDockerE2EDaemonImageEnv)
	}
	guestImage := strings.TrimSpace(os.Getenv(imageDockerE2EGuestImageEnv))
	if guestImage == "" {
		t.Fatalf("%s must name a local guest image", imageDockerE2EGuestImageEnv)
	}
	dockerSocket := strings.TrimSpace(os.Getenv(imageDockerE2ESocketEnv))
	if dockerSocket == "" {
		dockerSocket = "/var/run/docker.sock"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	dockerClient := newE2EDockerClient(t, ctx, daemonImage, guestImage)
	t.Cleanup(func() { _ = dockerClient.Close() })
	fixture := startImageDockerLifecycleDaemon(t, ctx, dockerClient, daemonImage, guestImage, dockerSocket)
	assertImageDockerDaemonUnprivileged(t, ctx, fixture)
	assertImageDockerDaemonSocketMount(t, ctx, fixture, dockerSocket)
	version := waitForImageDockerVersion(t, ctx, fixture)
	assertImageDockerVersion(t, fixture, version, "linux", imageDockerImageArchitecture(t, ctx, dockerClient, daemonImage))

	httpClient := imageDockerHTTPClient(0, fixture.tls)
	projectClient := agentcomposev2connect.NewProjectServiceClient(httpClient, fixture.baseURL)
	runClient := agentcomposev2connect.NewRunServiceClient(httpClient, fixture.baseURL)
	execClient := agentcomposev2connect.NewExecServiceClient(httpClient, fixture.baseURL)
	sandboxClient := agentcomposev2connect.NewSandboxServiceClient(httpClient, fixture.baseURL)

	projectResp, err := projectClient.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: &agentcomposev2.ProjectSpec{
			Name: "image-docker-e2e",
			Agents: []*agentcomposev2.AgentSpec{{
				Name:     "lifecycle",
				Provider: "codex",
				Image:    guestImage,
				Sandbox: &agentcomposev2.SandboxSpec{
					StoppedRuntimePolicy: domain.StoppedRuntimePolicyRetain,
				},
				Driver: &agentcomposev2.DriverSpec{
					Name:   "docker",
					Config: &agentcomposev2.DriverSpec_Docker{Docker: &agentcomposev2.DockerDriverSpec{}},
				},
			}},
		},
		Source: &agentcomposev2.ProjectSource{
			ComposePath: "/data/image-docker-e2e/agent-compose.yml",
			ProjectDir:  "/data/image-docker-e2e",
		},
	}))
	if err != nil {
		failImageDockerFixture(t, fixture, "ApplyProject returned error: %v", err)
	}
	if !projectResp.Msg.GetApplied() || projectResp.Msg.GetProject().GetSummary().GetProjectId() == "" {
		failImageDockerFixture(t, fixture, "ApplyProject response = %#v; issues: %s", projectResp.Msg, formatE2EProjectIssues(projectResp.Msg.GetIssues()))
	}
	runResp, err := runClient.RunAgent(ctx, connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectResp.Msg.GetProject().GetSummary().GetProjectId(),
		AgentName:       "lifecycle",
		Command:         "true",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
		ClientRequestId: "image-docker-e2e-" + strconv.FormatInt(time.Now().UnixNano(), 10),
	}))
	if err != nil {
		failImageDockerFixture(t, fixture, "RunAgent returned error: %v", err)
	}
	runSummary := runResp.Msg.GetRun().GetSummary()
	fixture.sandboxID = runSummary.GetSandboxId()
	if fixture.sandboxID == "" || runSummary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		failImageDockerFixture(t, fixture, "RunAgent summary = %#v", runSummary)
	}
	sandboxResp, err := sandboxClient.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: fixture.sandboxID}))
	if err != nil {
		failImageDockerFixture(t, fixture, "GetSandbox returned error: %v", err)
	}
	summary := sandboxResp.Msg.GetSandbox()
	if summary.GetDriver() != "docker" || summary.GetStatus() != agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING {
		failImageDockerFixture(t, fixture, "GetSandbox summary = %#v", summary)
	}
	initialContainer := inspectImageDockerSandboxContainer(t, ctx, fixture)
	if initialContainer.State == nil || !initialContainer.State.Running {
		failImageDockerFixture(t, fixture, "created Docker sandbox %s is not running", initialContainer.ID)
	}

	firstExec := runImageDockerExec(t, ctx, fixture, execClient, "sh", "-lc", "printf image-docker-e2e > /workspace/lifecycle-marker && cat /workspace/lifecycle-marker")
	if !firstExec.GetSuccess() || firstExec.GetExitCode() != 0 || firstExec.GetStdout() != "image-docker-e2e" {
		failImageDockerFixture(t, fixture, "first Exec result = %#v", firstExec)
	}

	stopResp, err := sandboxClient.StopSandbox(ctx, connect.NewRequest(&agentcomposev2.StopSandboxRequest{SandboxId: fixture.sandboxID}))
	if err != nil {
		failImageDockerFixture(t, fixture, "StopSandbox returned error: %v", err)
	}
	stopped := stopResp.Msg.GetSandbox()
	if got := stopped.GetStatus(); got != agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED {
		failImageDockerFixture(t, fixture, "StopSandbox status = %q, want %q", got, agentcomposev2.SandboxStatus_SANDBOX_STATUS_STOPPED)
	}
	if stopped.GetStoppedRuntimePolicy() != domain.StoppedRuntimePolicyRetain || stopped.GetStoppedRuntimeState() != domain.StoppedRuntimeStateRetained {
		failImageDockerFixture(t, fixture, "StopSandbox policy/state = %q/%q, want retain/retained", stopped.GetStoppedRuntimePolicy(), stopped.GetStoppedRuntimeState())
	}
	stoppedContainer := inspectImageDockerSandboxContainer(t, ctx, fixture)
	if stoppedContainer.ID != initialContainer.ID || stoppedContainer.State == nil || stoppedContainer.State.Running {
		failImageDockerFixture(t, fixture, "stopped Docker sandbox = id %s state %#v, want existing stopped %s", stoppedContainer.ID, stoppedContainer.State, initialContainer.ID)
	}

	resumeResp, err := sandboxClient.ResumeSandbox(ctx, connect.NewRequest(&agentcomposev2.ResumeSandboxRequest{SandboxId: fixture.sandboxID}))
	if err != nil {
		failImageDockerFixture(t, fixture, "ResumeSandbox returned error: %v", err)
	}
	if got := resumeResp.Msg.GetSandbox().GetStatus(); got != agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING {
		failImageDockerFixture(t, fixture, "ResumeSandbox status = %q, want %q", got, agentcomposev2.SandboxStatus_SANDBOX_STATUS_RUNNING)
	}
	resumedContainer := inspectImageDockerSandboxContainer(t, ctx, fixture)
	if resumedContainer.ID != initialContainer.ID || resumedContainer.State == nil || !resumedContainer.State.Running {
		failImageDockerFixture(t, fixture, "resumed Docker sandbox = id %s state %#v, want existing running %s", resumedContainer.ID, resumedContainer.State, initialContainer.ID)
	}

	secondExec := runImageDockerExec(t, ctx, fixture, execClient, "cat", "/workspace/lifecycle-marker")
	if !secondExec.GetSuccess() || secondExec.GetExitCode() != 0 || secondExec.GetStdout() != "image-docker-e2e" {
		failImageDockerFixture(t, fixture, "resumed Exec result = %#v", secondExec)
	}

	removeResp, err := sandboxClient.RemoveSandbox(ctx, connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{
		SandboxId: fixture.sandboxID,
		Force:     true,
	}))
	if err != nil {
		failImageDockerFixture(t, fixture, "RemoveSandbox returned error: %v", err)
	}
	if !removeResp.Msg.GetStopped() || !removeResp.Msg.GetRemoved() || removeResp.Msg.GetSandboxId() != fixture.sandboxID {
		failImageDockerFixture(t, fixture, "RemoveSandbox response = %#v", removeResp.Msg)
	}
	if _, err := sandboxClient.GetSandbox(ctx, connect.NewRequest(&agentcomposev2.GetSandboxRequest{SandboxId: fixture.sandboxID})); connect.CodeOf(err) != connect.CodeNotFound {
		failImageDockerFixture(t, fixture, "GetSandbox after remove error = %v, want not_found", err)
	}
	assertNoImageDockerSandboxContainers(t, ctx, fixture)
	assertImageDockerNativeHomesUninitialized(t, ctx, fixture)

	fixture.cleanup(t)
	assertImageDockerFixtureRemoved(t, fixture)
}

func runImageDockerVersionCommand(t *testing.T, ctx context.Context, dockerClient *client.Client, image string) imageDockerBuildInfo {
	t.Helper()
	name := imageDockerResourceName("version")
	createResp, err := dockerClient.ContainerCreate(ctx, &containerapi.Config{
		Image:  image,
		Cmd:    []string{"--json", "version"},
		Labels: imageDockerLabels(name),
	}, &containerapi.HostConfig{AutoRemove: false}, nil, nil, name)
	if err != nil {
		t.Fatalf("create daemon image version container: %v", err)
	}
	fixture := &imageDockerFixture{docker: dockerClient, containerID: createResp.ID}
	t.Cleanup(func() { fixture.cleanup(t) })
	if err := dockerClient.ContainerStart(ctx, createResp.ID, containerapi.StartOptions{}); err != nil {
		failImageDockerFixture(t, fixture, "start daemon image version container: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		info, err := dockerClient.ContainerInspect(ctx, createResp.ID)
		if err != nil {
			failImageDockerFixture(t, fixture, "inspect daemon image version container: %v", err)
		}
		if info.State != nil && !info.State.Running {
			if info.State.ExitCode != 0 {
				failImageDockerFixture(t, fixture, "daemon image --json version exit code = %d", info.State.ExitCode)
			}
			stdout, stderr := fixture.logStreams(t)
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
				failImageDockerFixture(t, fixture, "decode daemon image --json version: %v; stdout=%q stderr=%q", err, stdout, stderr)
			}
			wantKeys := []string{"arch", "compiled_drivers", "os", "version"}
			gotKeys := make([]string, 0, len(raw))
			for key := range raw {
				gotKeys = append(gotKeys, key)
			}
			slices.Sort(gotKeys)
			if !reflect.DeepEqual(gotKeys, wantKeys) {
				failImageDockerFixture(t, fixture, "daemon image --json version keys = %v, want %v", gotKeys, wantKeys)
			}
			var buildInfo imageDockerBuildInfo
			if err := json.Unmarshal([]byte(stdout), &buildInfo); err != nil {
				failImageDockerFixture(t, fixture, "decode daemon image build info: %v", err)
			}
			fixture.cleanup(t)
			assertImageDockerFixtureRemoved(t, fixture)
			return buildInfo
		}
		time.Sleep(50 * time.Millisecond)
	}
	failImageDockerFixture(t, fixture, "daemon image --json version timed out")
	return imageDockerBuildInfo{}
}

func newImageDockerTLSConfig(t *testing.T) imageDockerTLSConfig {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test TLS private key: %v", err)
	}
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate test TLS certificate serial: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serialNumber,
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create test TLS certificate: %v", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal test TLS private key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	certPath := filepath.Join(t.TempDir(), "server.crt")
	keyPath := filepath.Join(filepath.Dir(certPath), "server.key")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write test TLS certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write test TLS private key: %v", err)
	}
	return imageDockerTLSConfig{certPath: certPath, keyPath: keyPath, caPEM: certPEM, authToken: "image-docker-test-token"}
}

func startImageDockerStartupDaemon(t *testing.T, ctx context.Context, dockerClient *client.Client, image string) *imageDockerFixture {
	t.Helper()
	port := nat.Port(imageDockerDaemonPort)
	name := imageDockerResourceName("startup")
	tlsConfig := newImageDockerTLSConfig(t)
	createResp, err := dockerClient.ContainerCreate(ctx, &containerapi.Config{
		Image: image,
		Env: []string{
			"AGENT_COMPOSE_AUTH_TOKEN=" + tlsConfig.authToken,
			"HTTP_TLS_CERT_FILE=/tmp/agent-compose-server.crt",
			"HTTP_TLS_KEY_FILE=/tmp/agent-compose-server.key",
		},
		ExposedPorts: nat.PortSet{port: struct{}{}},
		Labels:       imageDockerLabels(name),
	}, &containerapi.HostConfig{
		AutoRemove:   false,
		PortBindings: nat.PortMap{port: []nat.PortBinding{{HostIP: "127.0.0.1"}}},
		Tmpfs:        map[string]string{"/data": "rw"},
		Mounts: []mountapi.Mount{
			{Type: mountapi.TypeBind, Source: tlsConfig.certPath, Target: "/tmp/agent-compose-server.crt", ReadOnly: true},
			{Type: mountapi.TypeBind, Source: tlsConfig.keyPath, Target: "/tmp/agent-compose-server.key", ReadOnly: true},
		},
	}, nil, nil, name)
	if err != nil {
		t.Fatalf("create no-KVM daemon container: %v", err)
	}
	fixture := &imageDockerFixture{docker: dockerClient, containerID: createResp.ID, tls: tlsConfig}
	t.Cleanup(func() { fixture.cleanup(t) })
	if err := dockerClient.ContainerStart(ctx, createResp.ID, containerapi.StartOptions{}); err != nil {
		failImageDockerFixture(t, fixture, "start no-KVM daemon container: %v", err)
	}
	fixture.baseURL = imageDockerDaemonBaseURL(t, ctx, fixture)
	return fixture
}

func startImageDockerLifecycleDaemon(t *testing.T, ctx context.Context, dockerClient *client.Client, daemonImage, guestImage, dockerSocket string) *imageDockerFixture {
	t.Helper()
	name := imageDockerResourceName("lifecycle")
	tlsConfig := newImageDockerTLSConfig(t)
	networkResp, err := dockerClient.NetworkCreate(ctx, name, networkapi.CreateOptions{
		Driver: "bridge",
		Labels: imageDockerLabels(name),
	})
	if err != nil {
		t.Fatalf("create image Docker E2E network: %v", err)
	}
	fixture := &imageDockerFixture{
		docker:      dockerClient,
		networkID:   networkResp.ID,
		networkName: name,
		tls:         tlsConfig,
	}
	t.Cleanup(func() { fixture.cleanup(t) })
	volume, err := dockerClient.VolumeCreate(ctx, volumeapi.CreateOptions{
		Name:   name,
		Labels: imageDockerLabels(name),
	})
	if err != nil {
		failImageDockerFixture(t, fixture, "create image Docker E2E data volume: %v", err)
	}
	fixture.volumeName = volume.Name
	port := nat.Port(imageDockerDaemonPort)
	createResp, err := dockerClient.ContainerCreate(ctx, &containerapi.Config{
		Image: daemonImage,
		Env: []string{
			"AGENT_COMPOSE_SOCKET=/data/agent-compose.sock",
			"AGENT_COMPOSE_AUTH_TOKEN=" + tlsConfig.authToken,
			"AUTH_PASSWORD=",
			"AUTH_USERNAME=",
			"DATA_ROOT=/data",
			"DEFAULT_IMAGE=" + guestImage,
			"DOCKER_DEFAULT_IMAGE=" + guestImage,
			"DOCKER_HOST=unix:///var/run/docker.sock",
			"HTTP_LISTEN=0.0.0.0:7410",
			"HTTP_TLS_CERT_FILE=/tmp/agent-compose-server.crt",
			"HTTP_TLS_KEY_FILE=/tmp/agent-compose-server.key",
			"LLM_API_ENDPOINT=",
			"LLM_API_KEY=",
			"OPENAI_API_KEY=",
			"RUNTIME_DRIVER=docker",
			"SANDBOX_ROOT=/data/sandboxes",
			"SANDBOX_START_TIMEOUT=2m",
			"SANDBOX_STOP_TIMEOUT=30s",
		},
		ExposedPorts: nat.PortSet{port: struct{}{}},
		Labels:       imageDockerLabels(name),
	}, &containerapi.HostConfig{
		AutoRemove:  false,
		NetworkMode: containerapi.NetworkMode(name),
		Mounts: []mountapi.Mount{
			{Type: mountapi.TypeBind, Source: dockerSocket, Target: "/var/run/docker.sock"},
			{Type: mountapi.TypeVolume, Source: volume.Name, Target: "/data"},
			{Type: mountapi.TypeBind, Source: tlsConfig.certPath, Target: "/tmp/agent-compose-server.crt", ReadOnly: true},
			{Type: mountapi.TypeBind, Source: tlsConfig.keyPath, Target: "/tmp/agent-compose-server.key", ReadOnly: true},
		},
		PortBindings: nat.PortMap{port: []nat.PortBinding{{HostIP: "127.0.0.1"}}},
	}, nil, nil, name)
	if err != nil {
		failImageDockerFixture(t, fixture, "create lifecycle daemon container: %v", err)
	}
	fixture.containerID = createResp.ID
	if err := dockerClient.ContainerStart(ctx, createResp.ID, containerapi.StartOptions{}); err != nil {
		failImageDockerFixture(t, fixture, "start lifecycle daemon container: %v", err)
	}
	fixture.baseURL = imageDockerDaemonBaseURL(t, ctx, fixture)
	return fixture
}

func imageDockerResourceName(kind string) string {
	return fmt.Sprintf("agent-compose-image-docker-e2e-%s-%d", kind, time.Now().UnixNano())
}

func imageDockerLabels(run string) map[string]string {
	taskRun := strings.TrimSpace(os.Getenv(imageDockerE2ERunIDEnv))
	if taskRun == "" {
		taskRun = "pid-" + strconv.Itoa(os.Getpid())
	}
	return map[string]string{
		imageDockerE2ELabel:               imageDockerE2ELabelValue,
		imageDockerE2ELabel + ".run":      run,
		imageDockerE2ELabel + ".task_run": taskRun,
	}
}

func imageDockerDaemonBaseURL(t *testing.T, ctx context.Context, fixture *imageDockerFixture) string {
	t.Helper()
	info, err := fixture.docker.ContainerInspect(ctx, fixture.containerID)
	if err != nil {
		failImageDockerFixture(t, fixture, "inspect daemon container port: %v", err)
	}
	port := nat.Port(imageDockerDaemonPort)
	bindings := info.NetworkSettings.Ports[port]
	for _, binding := range bindings {
		if binding.HostIP != "127.0.0.1" {
			continue
		}
		hostPort, err := strconv.Atoi(binding.HostPort)
		if err == nil && hostPort > 0 {
			return "https://127.0.0.1:" + strconv.Itoa(hostPort)
		}
	}
	failImageDockerFixture(t, fixture, "daemon port bindings for %s = %#v", port, bindings)
	return ""
}

func imageDockerHTTPClient(timeout time.Duration, tlsConfig imageDockerTLSConfig) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(tlsConfig.caPEM) {
		panic("invalid image Docker E2E CA certificate")
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	return &http.Client{Timeout: timeout, Transport: imageDockerAuthTransport{base: transport, token: tlsConfig.authToken}}
}

type imageDockerAuthTransport struct {
	base  http.RoundTripper
	token string
}

func (t imageDockerAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(request)
}

func assertImageDockerAuthEnforced(t *testing.T, ctx context.Context, fixture *imageDockerFixture) {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(fixture.tls.caPEM) {
		t.Fatal("invalid image Docker E2E CA certificate")
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	for _, token := range []string{"", "wrong-image-docker-token"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture.baseURL+"/api/version", nil)
		if err != nil {
			t.Fatalf("create unauthenticated request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			failImageDockerFixture(t, fixture, "unauthenticated /api/version request: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
			failImageDockerFixture(t, fixture, "unauthenticated /api/version status = %d, want 401 or 403", resp.StatusCode)
		}
	}
}

func waitForImageDockerVersion(t *testing.T, ctx context.Context, fixture *imageDockerFixture) imageDockerVersionEnvelope {
	t.Helper()
	client := imageDockerHTTPClient(time.Second, fixture.tls)
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	var lastBody bytes.Buffer
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture.baseURL+"/api/version", nil)
		if err == nil {
			resp, requestErr := client.Do(req)
			if requestErr == nil {
				lastBody.Reset()
				_, _ = lastBody.ReadFrom(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var envelope imageDockerVersionEnvelope
					if decodeErr := json.Unmarshal(lastBody.Bytes(), &envelope); decodeErr == nil {
						assertImageDockerAuthEnforced(t, ctx, fixture)
						return envelope
					} else {
						lastErr = decodeErr
					}
				} else {
					lastErr = fmt.Errorf("status %d", resp.StatusCode)
				}
			} else {
				lastErr = requestErr
			}
		}
		info, inspectErr := fixture.docker.ContainerInspect(ctx, fixture.containerID)
		if inspectErr == nil && info.State != nil && !info.State.Running {
			failImageDockerFixture(t, fixture, "daemon exited before readiness: state=%#v last_error=%v last_body=%q", info.State, lastErr, lastBody.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	failImageDockerFixture(t, fixture, "daemon did not become ready: last_error=%v last_body=%q", lastErr, lastBody.String())
	return imageDockerVersionEnvelope{}
}

func assertImageDockerBuildInfo(t *testing.T, buildInfo imageDockerBuildInfo, wantOS, wantArch string) {
	t.Helper()
	wantDrivers := []string{"docker", "boxlite", "microsandbox", "k8s"}
	if buildInfo.Version == "" || buildInfo.OS != wantOS || buildInfo.Arch != wantArch || !reflect.DeepEqual(buildInfo.CompiledDrivers, wantDrivers) {
		t.Fatalf("daemon image --json version = %#v, want %s/%s with drivers %v", buildInfo, wantOS, wantArch, wantDrivers)
	}
}

func assertImageDockerVersion(t *testing.T, fixture *imageDockerFixture, envelope imageDockerVersionEnvelope, wantOS, wantArch string) {
	t.Helper()
	wantDrivers := []string{"docker", "boxlite", "microsandbox", "k8s"}
	if string(envelope.Err) != "null" || envelope.Msg != "OK" {
		failImageDockerFixture(t, fixture, "/api/version envelope = err %s msg %q", envelope.Err, envelope.Msg)
	}
	data := envelope.Data
	if data.Version == "" || data.OS != wantOS || data.Arch != wantArch || !reflect.DeepEqual(data.CompiledDrivers, wantDrivers) || data.Timestamp <= 0 || data.Timezone == "" || data.TimezoneOffset == nil {
		failImageDockerFixture(t, fixture, "/api/version data = %#v, want %s/%s with drivers %v and legacy time fields", data, wantOS, wantArch, wantDrivers)
	}
}

func assertImageDockerDefaultDriver(t *testing.T, ctx context.Context, fixture *imageDockerFixture) {
	t.Helper()
	info, err := fixture.docker.ContainerInspect(ctx, fixture.containerID)
	if err != nil {
		failImageDockerFixture(t, fixture, "inspect startup daemon environment: %v", err)
	}
	var values []string
	if info.Config != nil {
		for _, item := range info.Config.Env {
			if strings.HasPrefix(item, "RUNTIME_DRIVER=") {
				values = append(values, strings.TrimPrefix(item, "RUNTIME_DRIVER="))
			}
		}
	}
	if !reflect.DeepEqual(values, []string{"docker"}) {
		failImageDockerFixture(t, fixture, "startup daemon RUNTIME_DRIVER values = %v, want [docker]", values)
	}
}

func assertImageDockerDaemonUnprivileged(t *testing.T, ctx context.Context, fixture *imageDockerFixture) {
	t.Helper()
	info, err := fixture.docker.ContainerInspect(ctx, fixture.containerID)
	if err != nil {
		failImageDockerFixture(t, fixture, "inspect daemon security configuration: %v", err)
	}
	if info.HostConfig == nil {
		failImageDockerFixture(t, fixture, "daemon has no HostConfig")
	}
	if info.HostConfig.Privileged || len(info.HostConfig.Resources.Devices) != 0 {
		failImageDockerFixture(t, fixture, "daemon security configuration = privileged %v devices %#v", info.HostConfig.Privileged, info.HostConfig.Resources.Devices)
	}
	for _, mount := range info.Mounts {
		if mount.Source == "/dev/kvm" || mount.Destination == "/dev/kvm" {
			failImageDockerFixture(t, fixture, "daemon unexpectedly mounts /dev/kvm: %#v", mount)
		}
	}
	if exitCode := imageDockerContainerExecExitCode(t, ctx, fixture, "sh", "-ec", "test ! -e /dev/kvm"); exitCode != 0 {
		failImageDockerFixture(t, fixture, "daemon sees /dev/kvm; probe exit code = %d", exitCode)
	}
}

func assertImageDockerStartupHasNoSocket(t *testing.T, ctx context.Context, fixture *imageDockerFixture) {
	t.Helper()
	info, err := fixture.docker.ContainerInspect(ctx, fixture.containerID)
	if err != nil {
		failImageDockerFixture(t, fixture, "inspect startup daemon mounts: %v", err)
	}
	for _, mount := range info.Mounts {
		if mount.Destination == "/var/run/docker.sock" {
			failImageDockerFixture(t, fixture, "startup daemon unexpectedly mounts Docker socket: %#v", mount)
		}
	}
	if exitCode := imageDockerContainerExecExitCode(t, ctx, fixture, "sh", "-ec", "test ! -S /var/run/docker.sock"); exitCode != 0 {
		failImageDockerFixture(t, fixture, "startup daemon sees a Docker socket; probe exit code = %d", exitCode)
	}
}

func assertImageDockerDaemonSocketMount(t *testing.T, ctx context.Context, fixture *imageDockerFixture, source string) {
	t.Helper()
	info, err := fixture.docker.ContainerInspect(ctx, fixture.containerID)
	if err != nil {
		failImageDockerFixture(t, fixture, "inspect lifecycle daemon mounts: %v", err)
	}
	for _, mount := range info.Mounts {
		if mount.Destination == "/var/run/docker.sock" && mount.Source == source && mount.Type == mountapi.TypeBind {
			return
		}
	}
	failImageDockerFixture(t, fixture, "lifecycle daemon does not mount Docker socket %s at /var/run/docker.sock: %#v", source, info.Mounts)
}

func assertImageDockerNativeHomesUninitialized(t *testing.T, ctx context.Context, fixture *imageDockerFixture) {
	t.Helper()
	probe := `for dir in /data/boxlite /data/microsandbox; do
  test -d "$dir"
  test -z "$(find "$dir" -mindepth 1 -print -quit)"
done
test ! -e /root/.microsandbox`
	if exitCode := imageDockerContainerExecExitCode(t, ctx, fixture, "sh", "-ec", probe); exitCode != 0 {
		failImageDockerFixture(t, fixture, "native runtime home probe exit code = %d; BoxLite or Microsandbox initialized unexpectedly", exitCode)
	}
}

func imageDockerContainerExecExitCode(t *testing.T, ctx context.Context, fixture *imageDockerFixture, command ...string) int {
	t.Helper()
	execResp, err := fixture.docker.ContainerExecCreate(ctx, fixture.containerID, containerapi.ExecOptions{Cmd: command})
	if err != nil {
		failImageDockerFixture(t, fixture, "create daemon exec probe: %v", err)
	}
	if err := fixture.docker.ContainerExecStart(ctx, execResp.ID, containerapi.ExecStartOptions{Detach: true}); err != nil {
		failImageDockerFixture(t, fixture, "start daemon exec probe: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		info, err := fixture.docker.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			failImageDockerFixture(t, fixture, "inspect daemon exec probe: %v", err)
		}
		if !info.Running {
			return info.ExitCode
		}
		time.Sleep(50 * time.Millisecond)
	}
	failImageDockerFixture(t, fixture, "daemon exec probe timed out")
	return -1
}

func runImageDockerExec(t *testing.T, ctx context.Context, fixture *imageDockerFixture, execClient agentcomposev2connect.ExecServiceClient, command string, args ...string) *agentcomposev2.ExecResult {
	t.Helper()
	resp, err := execClient.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: fixture.sandboxID},
		Command: &agentcomposev2.ExecCommand{
			Command: command,
			Args:    args,
		},
		TimeoutMs:      30_000,
		MaxOutputBytes: 1 << 20,
	}))
	if err != nil {
		failImageDockerFixture(t, fixture, "Exec(%s) returned error: %v", command, err)
	}
	return resp.Msg.GetResult()
}

func inspectImageDockerSandboxContainer(t *testing.T, ctx context.Context, fixture *imageDockerFixture) containerapi.InspectResponse {
	t.Helper()
	info, err := findE2EDockerSandboxContainer(ctx, fixture.docker, fixture.sandboxID)
	if err != nil {
		failImageDockerFixture(t, fixture, "%v", err)
	}
	return info
}

func imageDockerImageArchitecture(t *testing.T, ctx context.Context, dockerClient *client.Client, image string) string {
	t.Helper()
	info, _, err := dockerClient.ImageInspectWithRaw(ctx, image)
	if err != nil {
		t.Fatalf("inspect image architecture for %q: %v", image, err)
	}
	return info.Architecture
}

func (fixture *imageDockerFixture) cleanup(t *testing.T) {
	t.Helper()
	if fixture == nil || fixture.docker == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if fixture.sandboxID != "" {
		removeE2EDockerSandboxFallback(t, ctx, fixture.docker, fixture.sandboxID)
	}
	fixture.removeSandboxesOnFixtureNetwork(t, ctx)
	if fixture.containerID != "" {
		if err := fixture.docker.ContainerRemove(ctx, fixture.containerID, containerapi.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove image Docker E2E daemon %s: %v", fixture.containerID, err)
		}
	}
	if fixture.networkID != "" {
		if err := fixture.docker.NetworkRemove(ctx, fixture.networkID); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove image Docker E2E network %s: %v", fixture.networkID, err)
		}
	}
	if fixture.volumeName != "" {
		if err := fixture.docker.VolumeRemove(ctx, fixture.volumeName, true); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove image Docker E2E volume %s: %v", fixture.volumeName, err)
		}
	}
}

func (fixture *imageDockerFixture) removeSandboxesOnFixtureNetwork(t *testing.T, ctx context.Context) {
	t.Helper()
	if fixture.networkName == "" {
		return
	}
	items, err := fixture.docker.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "agent-compose.driver=docker"))})
	if err != nil {
		t.Logf("fallback image Docker sandbox lookup failed: %v", err)
		return
	}
	for _, item := range items {
		info, err := fixture.docker.ContainerInspect(ctx, item.ID)
		if err != nil {
			continue
		}
		owned := false
		if info.NetworkSettings != nil {
			_, owned = info.NetworkSettings.Networks[fixture.networkName]
		}
		if owned {
			if err := fixture.docker.ContainerRemove(ctx, item.ID, containerapi.RemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Logf("fallback removal failed for image Docker sandbox %s: %v", item.ID, err)
			}
		}
	}
}

func assertNoImageDockerSandboxContainers(t *testing.T, ctx context.Context, fixture *imageDockerFixture) {
	t.Helper()
	items, err := fixture.docker.ContainerList(ctx, containerapi.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "agent-compose.sandbox_id="+fixture.sandboxID))})
	if err != nil {
		failImageDockerFixture(t, fixture, "list removed Docker sandbox containers: %v", err)
	}
	if len(items) != 0 {
		failImageDockerFixture(t, fixture, "Docker sandbox %s left containers: %#v", fixture.sandboxID, items)
	}
}

func assertImageDockerFixtureRemoved(t *testing.T, fixture *imageDockerFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if fixture.containerID != "" {
		if _, err := fixture.docker.ContainerInspect(ctx, fixture.containerID); !cerrdefs.IsNotFound(err) {
			t.Fatalf("daemon container %s still exists or inspect failed: %v", fixture.containerID, err)
		}
	}
	if fixture.networkID != "" {
		if _, err := fixture.docker.NetworkInspect(ctx, fixture.networkID, networkapi.InspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Fatalf("E2E network %s still exists or inspect failed: %v", fixture.networkID, err)
		}
	}
	if fixture.volumeName != "" {
		if _, err := fixture.docker.VolumeInspect(ctx, fixture.volumeName); !cerrdefs.IsNotFound(err) {
			t.Fatalf("E2E volume %s still exists or inspect failed: %v", fixture.volumeName, err)
		}
	}
	if fixture.sandboxID != "" {
		assertNoImageDockerSandboxContainers(t, ctx, fixture)
	}
}

func failImageDockerFixture(t *testing.T, fixture *imageDockerFixture, format string, args ...any) {
	t.Helper()
	message := fmt.Sprintf(format, args...)
	diagnostics := ""
	if fixture != nil {
		diagnostics = fixture.diagnostics(t)
	}
	t.Fatalf("%s\n%s", message, diagnostics)
}

func (fixture *imageDockerFixture) logs(t *testing.T) string {
	t.Helper()
	stdout, stderr := fixture.logStreams(t)
	return stdout + stderr
}

func (fixture *imageDockerFixture) logStreams(t *testing.T) (string, string) {
	t.Helper()
	if fixture == nil || fixture.docker == nil || fixture.containerID == "" {
		return "", "<unavailable>"
	}
	return imageDockerContainerLogStreams(t, fixture.docker, fixture.containerID)
}

func imageDockerContainerLogStreams(t *testing.T, dockerClient *client.Client, containerID string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reader, err := dockerClient.ContainerLogs(ctx, containerID, containerapi.LogsOptions{ShowStdout: true, ShowStderr: true, Tail: "200"})
	if err != nil {
		return "", "<read error: " + err.Error() + ">"
	}
	defer func() { _ = reader.Close() }()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, reader); err != nil {
		return "", "<decode error: " + err.Error() + ">"
	}
	return stdout.String(), stderr.String()
}

func (fixture *imageDockerFixture) diagnostics(t *testing.T) string {
	t.Helper()
	if fixture == nil || fixture.docker == nil {
		return "fixture diagnostics: <unavailable>"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output strings.Builder
	if fixture.containerID != "" {
		if info, err := fixture.docker.ContainerInspect(ctx, fixture.containerID); err != nil {
			fmt.Fprintf(&output, "daemon inspect error: %v\n", err)
		} else {
			privileged := false
			var devices []containerapi.DeviceMapping
			if info.HostConfig != nil {
				privileged = info.HostConfig.Privileged
				devices = info.HostConfig.Resources.Devices
			}
			fmt.Fprintf(&output, "daemon inspect: id=%s state=%#v privileged=%v devices=%#v mounts=%#v\n", info.ID, info.State, privileged, devices, info.Mounts)
		}
		stdout, stderr := fixture.logStreams(t)
		fmt.Fprintf(&output, "daemon stdout:\n%s\ndaemon stderr:\n%s\n", stdout, stderr)
	}
	if fixture.sandboxID != "" {
		if info, err := findE2EDockerSandboxContainer(ctx, fixture.docker, fixture.sandboxID); err != nil {
			fmt.Fprintf(&output, "sandbox inspect error: %v\n", err)
		} else {
			fmt.Fprintf(&output, "sandbox inspect: id=%s state=%#v mounts=%#v networks=%#v\n", info.ID, info.State, info.Mounts, info.NetworkSettings)
			stdout, stderr := imageDockerContainerLogStreams(t, fixture.docker, info.ID)
			fmt.Fprintf(&output, "sandbox stdout:\n%s\nsandbox stderr:\n%s\n", stdout, stderr)
		}
	}
	if fixture.networkID != "" {
		if info, err := fixture.docker.NetworkInspect(ctx, fixture.networkID, networkapi.InspectOptions{}); err != nil {
			fmt.Fprintf(&output, "network inspect error: %v\n", err)
		} else {
			fmt.Fprintf(&output, "network inspect: id=%s name=%s containers=%d\n", info.ID, info.Name, len(info.Containers))
		}
	}
	if fixture.volumeName != "" {
		if info, err := fixture.docker.VolumeInspect(ctx, fixture.volumeName); err != nil {
			fmt.Fprintf(&output, "volume inspect error: %v\n", err)
		} else {
			fmt.Fprintf(&output, "volume inspect: name=%s mountpoint=%s\n", info.Name, info.Mountpoint)
		}
	}
	return output.String()
}
