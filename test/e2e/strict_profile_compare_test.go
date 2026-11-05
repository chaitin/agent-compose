package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// TestE2EStrictProfileCompare validates the opt-in egress profile on one driver.
// With SANDBOX_EGRESS_PROFILE unset the sandbox keeps the pre-PR topology and
// direct egress. With =strict it runs ac-init and the Docker supervisor, or the
// driver's structural default deny, while the declared policy is still decided
// by the engine.
func TestE2EStrictProfileCompare(t *testing.T) {
	driver := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_STRICT_COMPARE_DRIVER"))
	if driver == "" {
		t.Skip("set AGENT_COMPOSE_E2E_STRICT_COMPARE_DRIVER to run the strict-profile comparison")
	}
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_STRICT_COMPARE_IMAGE"))
	if image == "" {
		image = "agent-compose-guest:local"
	}
	host := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_STRICT_COMPARE_HOST"))
	if host == "" {
		host = networkPolicyE2EHostAddress(t, context.Background())
	}
	// The CLI speaks to the daemon over the host address. A proxy inherited from
	// the developer shell would send that request to the proxy instead, so keep
	// the test's own control plane out of it while leaving the daemon's egress
	// proxy configuration untouched.
	t.Setenv("NO_PROXY", "127.0.0.1,localhost,"+host)
	t.Setenv("no_proxy", "127.0.0.1,localhost,"+host)
	fixture := startStrictCompareFixture(t, host)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	binary := e2eDaemonBinary(t, ctx, repoRoot, t.TempDir())

	for _, strict := range []bool{false, true} {
		strict := strict
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			// SQLite and the sandbox working data are latency-sensitive; keep
			// them off a busy repository disk. The VM drivers need the larger
			// repository filesystem for their base/rootfs disks, so only the
			// container/API drivers move to the system temporary directory.
			rootBase := repoRoot
			if driver == "docker" || driver == "k8s" {
				rootBase = os.TempDir()
			}
			root, err := os.MkdirTemp(rootBase, ".strict-compare-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			dataRoot := filepath.Join(root, "data")
			projectRoot := filepath.Join(root, "project")
			if err := os.MkdirAll(dataRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(projectRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repoRoot, ".cache", "strict-compare", "images"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(repoRoot, ".cache", "strict-compare", "tmp"), 0o755); err != nil {
				t.Fatal(err)
			}
			address := unusedNetworkPolicyAddress(t, host)
			baseURL := "http://" + address
			extraEnv := map[string]string{
				"AGENT_COMPOSE_RUNTIME_BASE_URL": baseURL,
				"AGENT_COMPOSE_SOCKET":           filepath.Join(root, "agent-compose.sock"),
				"RUNTIME_DRIVER":                 driver,
				"NO_PROXY":                       "127.0.0.1,localhost",
				"no_proxy":                       "127.0.0.1,localhost",
				"STRICT_TEST_TOKEN":              "strict-secret-token",
				// The VM drivers materialize a base disk from the guest image on
				// first use. Keep the export off /tmp (a small tmpfs on many hosts)
				// and share it between the two subtests so it is built once.
				"IMAGE_CACHE_ROOT": filepath.Join(repoRoot, ".cache", "strict-compare", "images"),
				"TMPDIR":           filepath.Join(repoRoot, ".cache", "strict-compare", "tmp"),
				// microsandbox derives its runtime socket path from MSB_HOME,
				// and Unix socket paths must stay under 108 bytes.
				"MSB_HOME":              filepath.Join(os.TempDir(), "ac-msbhome"),
				"SANDBOX_START_TIMEOUT": "60m",
			}
			switch driver {
			case "docker":
			case "microsandbox":
				extraEnv["MICROSANDBOX_DEFAULT_IMAGE"] = image
				extraEnv["AGENT_COMPOSE_SANDBOX_HOST_MODE"] = "managed"
				extraEnv["AGENT_COMPOSE_SANDBOX_HOST_SOCKET"] = filepath.Join(os.TempDir(), fmt.Sprintf("strict-host-%d.sock", time.Now().UnixNano()))
			case "k8s":
				kubeconfig := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_STRICT_COMPARE_KUBECONFIG"))
				if kubeconfig == "" {
					kubeconfig = strings.TrimSpace(os.Getenv("KUBECONFIG"))
				}
				if kubeconfig == "" {
					kubeconfig = "/etc/rancher/k3s/k3s.yaml"
				}
				extraEnv["K8S_DEFAULT_IMAGE"] = image
				extraEnv["K8S_KUBECONFIG"] = kubeconfig
				extraEnv["K8S_NAMESPACE"] = "default"
			default:
				t.Skipf("unsupported driver %q", driver)
			}
			if strict {
				extraEnv["SANDBOX_EGRESS_PROFILE"] = "strict"
				extraEnv["SANDBOX_INIT_SOCKET_DIR"] = filepath.Join(os.TempDir(), fmt.Sprintf("aci-%d-%d", os.Getpid(), boolInt(strict)))
			}
			daemon := startE2EDaemonWithEnv(t, binary, repoRoot, dataRoot, address, image, extraEnv)
			waitForE2EDaemon(t, ctx, daemon, baseURL)
			t.Cleanup(func() { t.Logf("daemon log (strict=%v):\n%s", strict, daemon.logs.String()) })

			writeStrictCompareProject(t, projectRoot, driver, fixture.host, fixture.port)
			runNetworkPolicyE2ECLI(t, ctx, binary, baseURL, projectRoot, "up")

			openID := startStrictCompareSandbox(t, ctx, binary, baseURL, projectRoot, "open")
			defer stopStrictCompareSandbox(t, binary, baseURL, projectRoot, openID)
			lockedID := startStrictCompareSandbox(t, ctx, binary, baseURL, projectRoot, "locked")
			defer stopStrictCompareSandbox(t, binary, baseURL, projectRoot, lockedID)
			l7ID := startStrictCompareSandbox(t, ctx, binary, baseURL, projectRoot, "l7")
			defer stopStrictCompareSandbox(t, binary, baseURL, projectRoot, l7ID)

			curlCode := `curl -sS -m 20 -o /dev/null -w "code=%{http_code}"`
			if !strict {
				if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, openID, curlCode+" http://"+fixture.address()+"/open"); !strings.Contains(out, "code=200") {
					t.Fatalf("open sandbox could not reach the fixture directly: %q", out)
				}
				if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, openID, `env | grep -ci _proxy=`); strictCompareFirstLine(out) != "0" {
					t.Fatalf("open sandbox carries proxy variables without the profile: %q", out)
				}
				if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, lockedID, curlCode+" http://"+fixture.address()+"/open"); !strings.Contains(out, "code=200") {
					t.Fatalf("locked sandbox could not use the declared allowance: %q", out)
				}
				if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, lockedID, curlCode+" https://example.com/"); !strings.Contains(out, "403") {
					t.Fatalf("locked sandbox reached an undeclared host: %q", out)
				}
				if driver == "docker" {
					strictCompareDockerTopology(t, ctx, openID, false)
				}
				return
			}

			// The strict profile keeps an undeclared agent on an allow-all policy
			// that is still mediated, so enabling the profile does not break an
			// existing deployment.
			if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, openID, curlCode+" http://"+fixture.address()+"/open"); !strings.Contains(out, "code=200") {
				t.Fatalf("strict undeclared sandbox could not reach the fixture through the proxy: %q", out)
			}
			// init consumes the proxy environment for its own mediation and
			// deliberately does not expose it to the workload; the successful
			// request above is the contract, not the guest's environment.
			if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, openID, `env | grep -ci _proxy=`); strictCompareFirstLine(out) != "0" {
				t.Fatalf("strict open sandbox proxy variables = %q, want none in the mediated workload", out)
			}
			if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, lockedID, curlCode+" http://"+fixture.address()+"/open"); !strings.Contains(out, "code=200") {
				t.Fatalf("strict locked sandbox could not use the declared allowance: %q", out)
			}
			if out := strictCompareExec(t, ctx, binary, baseURL, projectRoot, lockedID, curlCode+" https://example.com/"); !strictCompareRefused(out) {
				t.Fatalf("strict locked sandbox reached an undeclared host: %q", out)
			}
			if driver == "docker" {
				strictCompareDockerTopology(t, ctx, openID, true)
			}
			strictCompareL7(t, ctx, binary, baseURL, projectRoot, l7ID, fixture)
			if driver == "k8s" {
				strictCompareK8sNetworkPolicy(t)
			}
		})
	}
}

// strictCompareFixture serves plain HTTP, records the fields L7 and header
// injection must expose, and receives UDP on a port the policy never allows.
type strictCompareFixture struct {
	host        string
	port        int
	udpConn     net.PacketConn
	udpPort     int
	udpReceived chan struct{}
}

func (f strictCompareFixture) address() string {
	return net.JoinHostPort(f.host, strconv.Itoa(f.port))
}

func (f strictCompareFixture) udpAddress() string {
	return net.JoinHostPort(f.host, strconv.Itoa(f.udpPort))
}

func startStrictCompareFixture(t *testing.T, host string) strictCompareFixture {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("listen on %s: %v", host, err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Seen-Method", r.Method)
			w.Header().Set("X-Seen-Token", r.Header.Get("X-Agent-Token"))
			_, _ = w.Write([]byte("method=" + r.Method + " path=" + r.URL.Path + " token=" + r.Header.Get("X-Agent-Token") + "\n"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	_, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	udpConn, err := net.ListenPacket("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("listen on UDP %s: %v", host, err)
	}
	t.Cleanup(func() { _ = udpConn.Close() })
	_, udpPortText, _ := net.SplitHostPort(udpConn.LocalAddr().String())
	udpPort, _ := strconv.Atoi(udpPortText)
	fixture := strictCompareFixture{
		host:        host,
		port:        port,
		udpConn:     udpConn,
		udpPort:     udpPort,
		udpReceived: make(chan struct{}, 8),
	}
	go func() {
		buffer := make([]byte, 256)
		for {
			if _, _, err := udpConn.ReadFrom(buffer); err != nil {
				return
			}
			select {
			case fixture.udpReceived <- struct{}{}:
			default:
			}
		}
	}()
	return fixture
}

func (f strictCompareFixture) waitForUDP(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-f.udpReceived:
		return true
	case <-timer.C:
		return false
	}
}

func (f strictCompareFixture) drainUDP() {
	for {
		select {
		case <-f.udpReceived:
		default:
			return
		}
	}
}

func writeStrictCompareProject(t *testing.T, projectRoot, driver, host string, port int) {
	t.Helper()
	allow := fmt.Sprintf("          - host: %s\n            port: %d\n", host, port)
	l7Allow := allow +
		"            methods: [GET]\n" +
		"            paths: [\"/ok*\"]\n" +
		"            inject_headers:\n" +
		"              X-Agent-Token: \"${STRICT_TEST_TOKEN}\"\n"
	project := "name: strict-compare\nagents:\n" +
		strictCompareAgent(driver, "open", "") +
		strictCompareAgent(driver, "locked", allow) +
		strictCompareAgent(driver, "l7", l7Allow)
	if err := os.WriteFile(filepath.Join(projectRoot, "agent-compose.yml"), []byte(project), 0o644); err != nil {
		t.Fatalf("write the strict comparison project: %v", err)
	}
}

func strictCompareAgent(driver, name, allowBody string) string {
	block := "  " + name + ":\n" +
		"    provider: codex\n" +
		"    system_prompt: |\n" +
		"      Answer with the single word requested and nothing else.\n"
	if allowBody != "" {
		block += "    policy:\n      network:\n        default: deny-all\n        allow:\n" + allowBody
	}
	return block + "    driver:\n      " + driver + ": {}\n"
}

func startStrictCompareSandbox(t *testing.T, ctx context.Context, binary, baseURL, projectRoot, agent string) string {
	t.Helper()
	output := runNetworkPolicyE2ECLI(t, ctx, binary, baseURL, projectRoot, "run", agent, "--keep-running", "--command", "true", "--json")
	var started struct {
		SandboxID string `json:"sandbox_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &started); err != nil || started.SandboxID == "" {
		t.Fatalf("decode the started sandbox from %q: %v", strings.TrimSpace(output), err)
	}
	return started.SandboxID
}

func stopStrictCompareSandbox(t *testing.T, binary, baseURL, projectRoot, sandboxID string) {
	if sandboxID == "" {
		return
	}
	for _, verb := range []string{"sandbox stop", "sandbox rm"} {
		command := exec.Command(binary, append([]string{"--host", baseURL}, append(strings.Fields(verb), sandboxID)...)...)
		command.Dir = projectRoot
		command.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_SOCKET": ""})
		_ = command.Run()
	}
}

func strictCompareExec(t *testing.T, ctx context.Context, binary, baseURL, projectRoot, sandboxID, command string) string {
	t.Helper()
	output, _ := execNetworkPolicyE2ECLI(ctx, binary, baseURL, projectRoot, "exec", sandboxID, "--command", command)
	return output
}

// strictCompareRefused reports the two shapes a refusal can take: the proxy's
// own 403 when the guest talks to it directly, or a closed mediated connection
// when init performed the CONNECT on the guest's behalf.
func strictCompareRefused(output string) bool {
	return strings.Contains(output, "403") || strings.Contains(output, "code=000") || strings.Contains(output, "Couldn't connect")
}

func strictCompareFirstLine(output string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(line)
}

func strictCompareDockerTopology(t *testing.T, ctx context.Context, sandboxID string, strict bool) {
	t.Helper()
	dockerClient := newE2EDockerClient(t, ctx)
	containers, err := dockerClient.ContainerList(ctx, containerapi.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "agent-compose.sandbox_id="+sandboxID)),
	})
	if err != nil {
		t.Fatalf("list the sandbox container: %v", err)
	}
	if len(containers) != 1 {
		t.Fatalf("sandbox %s containers = %d, want 1", sandboxID, len(containers))
	}
	inspect, err := dockerClient.ContainerInspect(ctx, containers[0].ID)
	if err != nil {
		t.Fatalf("inspect the sandbox container: %v", err)
	}
	pid1 := inspect.Path
	if strict {
		if !strings.Contains(pid1, "ac-init") {
			t.Fatalf("strict sandbox PID 1 = %q, want ac-init", pid1)
		}
		if inspect.HostConfig.NetworkMode != containerapi.NetworkMode("none") {
			t.Fatalf("strict sandbox network mode = %q, want none", inspect.HostConfig.NetworkMode)
		}
		if inspect.Config.Labels["agent-compose.sandbox_init"] != "true" {
			t.Fatalf("strict sandbox init label = %q", inspect.Config.Labels["agent-compose.sandbox_init"])
		}
		if inspect.Config.Labels["agent-compose.sandbox_supervisor"] != "true" {
			t.Fatalf("strict sandbox supervisor label = %q", inspect.Config.Labels["agent-compose.sandbox_supervisor"])
		}
		return
	}
	if strings.Contains(pid1, "ac-init") {
		t.Fatalf("default sandbox PID 1 = %q, want the pre-PR process model", pid1)
	}
	if inspect.HostConfig.NetworkMode == containerapi.NetworkMode("none") {
		t.Fatalf("default sandbox network mode is none; the pre-PR topology was changed")
	}
}

func strictCompareL7(t *testing.T, ctx context.Context, binary, baseURL, projectRoot, sandboxID string, fixture strictCompareFixture) {
	t.Helper()
	allowed := strictCompareExec(t, ctx, binary, baseURL, projectRoot, sandboxID,
		`curl -sS -m 20 http://`+fixture.address()+`/ok`)
	if !strings.Contains(allowed, "method=GET") || !strings.Contains(allowed, "token=strict-secret-token") {
		t.Fatalf("allowed L7 request = %q, want the injected header to reach the fixture", allowed)
	}
	deniedPath := strictCompareExec(t, ctx, binary, baseURL, projectRoot, sandboxID,
		`curl -sS -m 20 -o /dev/null -w "code=%{http_code}" http://`+fixture.address()+`/denied`)
	if !strings.Contains(deniedPath, "403") {
		t.Fatalf("denied L7 path = %q, want 403", deniedPath)
	}
	deniedMethod := strictCompareExec(t, ctx, binary, baseURL, projectRoot, sandboxID,
		`curl -sS -m 20 -X POST -o /dev/null -w "code=%{http_code}" http://`+fixture.address()+`/ok`)
	if !strings.Contains(deniedMethod, "403") {
		t.Fatalf("denied L7 method = %q, want 403", deniedMethod)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func strictCompareK8sNetworkPolicy(t *testing.T) {
	t.Helper()
	command := exec.Command("kubectl", "get", "networkpolicy", "-n", "default", "-o", "name")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("list the K8s NetworkPolicies: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "agent-compose") {
		t.Fatalf("no agent-compose NetworkPolicy found: %s", output)
	}
}
