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

	networkapi "github.com/docker/docker/api/types/network"
)

// dockerNetworkPolicyE2EImageEnv names the guest image the execution-matrix test
// runs in. The test needs a guest with curl and outbound network access: the
// declared-allowance row reaches a public host on purpose, so that the engine's
// proxy is what decides the connection.
const dockerNetworkPolicyE2EImageEnv = "AGENT_COMPOSE_E2E_NETWORK_POLICY_IMAGE"

// networkPolicyE2EDriverEnv selects the runtime driver the whole matrix runs
// against. The rows are the same for every driver; what differs is which
// mechanism enforces them, which is why the matrix runs per driver.
const networkPolicyE2EDriverEnv = "AGENT_COMPOSE_E2E_NETWORK_POLICY_DRIVER"

// networkPolicyE2EFixtureHostEnv names a host address the sandbox can reach but
// the daemon's proxy bypass list does not contain. The rows that name an address
// need it: a fixture on the proxy's own address never reaches the proxy, so
// those rows would pass or fail for a reason other than the declaration.
const networkPolicyE2EFixtureHostEnv = "AGENT_COMPOSE_E2E_NETWORK_POLICY_FIXTURE_HOST"

// TestE2EDockerNetworkPolicyExecutionMatrix states what a declared policy.network
// does on a driver, row by row:
//
//   - an agent that declared nothing keeps the egress the driver had before the
//     engine could decide it: unrestricted, and with no proxy in its environment;
//   - a declared allowance is tunnelled and succeeds (200);
//   - a host the policy does not allow is refused by the engine (403), before
//     any connection is opened;
//   - the allowance is a host and a port: the same host on a control-plane port
//     is refused whatever the policy says;
//   - an address named by the declaration is an entry of its own;
//   - `allowed_ips` ties a name to the address it may use, so an entry whose
//     range does not cover the destination is refused;
//   - the metadata address is refused even when the declaration names it and
//     covers its range;
//   - an allow list without `default: deny-all` restricts nothing, while a denial
//     under the allow-all default refuses only the destination it names;
//   - editing the declaration reaches a sandbox that is already running, because
//     the proxy re-reads the declaration per request;
//   - the daemon records the refusals, and each refusal names the engine rule
//     that produced it.
func TestE2EDockerNetworkPolicyExecutionMatrix(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerNetworkPolicyE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to a guest image with curl and network access", dockerNetworkPolicyE2EImageEnv)
	}
	driver := strings.TrimSpace(os.Getenv(networkPolicyE2EDriverEnv))
	if driver == "" {
		driver = "docker"
	}
	fixtureHost := strings.TrimSpace(os.Getenv(networkPolicyE2EFixtureHostEnv))
	if fixtureHost == "" {
		t.Skipf("set %s to a host address the sandbox reaches but the daemon's proxy bypass list does not cover", networkPolicyE2EFixtureHostEnv)
	}
	// A driver that boots a microVM spends much of each row in the guest's own
	// start, so the budget belongs to the driver rather than to one number.
	deadline := 25 * time.Minute
	if driver != "docker" {
		deadline = 55 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	root, err := os.MkdirTemp(repoRoot, ".netpol-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := startNetworkPolicyE2EFixture(t, fixtureHost)
	// Only the Docker driver consumes a Docker-local image; the KVM drivers pull
	// their guest image themselves, so requiring it in Docker would fail before
	// the driver under test ever ran.
	if driver == "docker" {
		newE2EDockerClient(t, ctx, image)
	}
	binary := e2eDaemonBinary(t, ctx, repoRoot, root)
	address := unusedNetworkPolicyAddress(t, networkPolicyE2EHostAddress(t, ctx))
	baseURL := "http://" + address
	// The sandbox reaches the proxy over the host address the sandbox network
	// exposes, and that is also the base URL the daemon hands the guest.
	// The control socket lives in the system temporary directory: a socket path
	// is limited to about 108 bytes and a checkout path can exceed it.
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("agent-compose-netpol-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	extraEnv := map[string]string{
		"AGENT_COMPOSE_RUNTIME_BASE_URL": baseURL,
		"AGENT_COMPOSE_SOCKET":           socketPath,
		"RUNTIME_DRIVER":                 driver,
	}
	if driver != "docker" {
		extraEnv[strings.ToUpper(driver)+"_DEFAULT_IMAGE"] = image
	}
	daemon := startE2EDaemonWithEnv(t, binary, repoRoot, dataRoot, address, image, extraEnv)
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	// The decisions the daemon recorded are the evidence this matrix is about, so
	// the log is reported whether the rows passed or failed.
	t.Cleanup(func() { t.Logf("network policy daemon log:\n%s", daemon.logs.String()) })

	project := networkPolicyE2EProject{driver: driver, fixtureHost: fixture.host, fixturePort: fixture.port}
	project.write(t, projectRoot, false)
	runNetworkPolicyE2ECLI(t, ctx, binary, baseURL, projectRoot, "up")

	const curlCode = `command -v curl >/dev/null || { echo NO_CURL; exit 9; }; curl -sS -m 20 -o /dev/null -w "code=%{http_code}"`
	guest := func(agent, url string) string {
		t.Helper()
		return runNetworkPolicyE2EGuest(t, ctx, binary, baseURL, projectRoot, "run", agent, "--rm", "--command", curlCode+" "+url+" 2>&1")
	}
	assertAllowed := func(agent, url string) {
		t.Helper()
		output := guest(agent, url)
		if status := networkPolicyE2EHTTPCode(t, output); !networkPolicyE2ESuccessfulCode(status) {
			t.Fatalf("%s reaching %s = %s, want a completed exchange because the declaration allows it", agent, url, status)
		}
	}
	// A refused HTTPS destination never produces an HTTP status: curl reports
	// 000 and names the proxy's 403 in its own error, so the refusal is read from
	// the response text. That covers both an inspected request the proxy answers
	// with 403 and a refused CONNECT tunnel.
	assertRefused := func(agent, url string) {
		t.Helper()
		output := guest(agent, url)
		if !strings.Contains(output, "403") {
			t.Fatalf("%s reaching %s = %q, want 403 from the engine", agent, url, strings.TrimSpace(output))
		}
	}

	// A declaration is only decided by the proxy when the guest actually uses
	// it: an address in the bypass list would leave the daemon nothing to
	// decide, and the rows below would report the driver's behavior instead.
	bypass := networkPolicyE2EGuestResult(t, ctx, binary, baseURL, projectRoot, "run", "matrix", "--rm", "--command", `printenv no_proxy`)
	if strings.Contains(bypass, fixture.host) {
		t.Skipf("the fixture host %s is in the daemon's proxy bypass list (%q)", fixture.host, bypass)
	}

	// No declaration keeps the egress the driver had before the engine could
	// decide it: the sandbox reaches the internet and an arbitrary address
	// directly, and no proxy is put in its environment.
	assertAllowed("open", "https://example.com/")
	assertAllowed("open", "http://"+fixture.address()+"/")
	if proxied := networkPolicyE2EGuestResult(t, ctx, binary, baseURL, projectRoot, "run", "open", "--rm", "--command", `env | grep -ci "_proxy="`); proxied != "0" {
		t.Fatalf("an agent that declared nothing carries a proxy: %q", proxied)
	}

	// An allow list under the default allow-all restricts nothing.
	assertAllowed("allow-list-only", "https://example.com/")

	// A denial restricts under the allow-all default: the denied destination is
	// refused, while a host the declaration does not name stays reachable.
	assertRefused("deny-list", "https://example.com/")
	assertAllowed("deny-list", "https://api.github.com/")

	// Partial allowance: the declared host passes, every other host is refused
	// before a connection is opened, and the sandbox carries the proxy.
	assertAllowed("matrix", "https://api.github.com/")
	assertRefused("matrix", "https://example.com/")
	if proxied := networkPolicyE2EGuestResult(t, ctx, binary, baseURL, projectRoot, "run", "matrix", "--rm", "--command", `env | grep -ci "_proxy="`); proxied != "6" {
		t.Fatalf("declared sandbox proxy environment = %q, want 6 variables", proxied)
	}

	// A URL entry is a host and port: the same host on the control-plane port
	// stays refused even though the policy names it.
	assertRefused("port-blocked", "https://api.github.com:6443/")

	// An address named by the declaration is an entry of its own: the address
	// passes, and a port the entry does not cover is refused.
	assertAllowed("address-literal", "http://"+fixture.address()+"/")
	assertRefused("address-literal", "http://"+fixture.uncoveredAddress()+"/")

	// `allowed_ips` ties a name to the address it may use: the address inside its
	// own range passes, while an entry whose range does not cover the destination
	// is refused.
	assertAllowed("address-constrained", "http://"+fixture.address()+"/")
	assertRefused("address-out-of-range", "http://"+fixture.address()+"/")
	assertRefused("name-out-of-range", "https://api.github.com/")

	// The metadata address stays refused whatever the declaration says, even
	// when it names the address and covers its range.
	assertRefused("metadata-allowed", "http://169.254.169.254/latest/meta-data/")

	// One running sandbox, two declarations: the proxy decides per request, so
	// the edit reaches the sandbox that is already up.
	sandboxID := networkPolicyE2ESandboxID(t, ctx, binary, baseURL, projectRoot)
	beforeEdit := runNetworkPolicyE2EGuest(t, ctx, binary, baseURL, projectRoot, "exec", sandboxID, "--command", curlCode+" https://example.com/ 2>&1")
	if !strings.Contains(beforeEdit, "403") {
		t.Fatalf("before the edit the undeclared host was not refused: %q", beforeEdit)
	}
	project.write(t, projectRoot, true)
	runNetworkPolicyE2ECLI(t, ctx, binary, baseURL, projectRoot, "up")
	afterEdit := runNetworkPolicyE2EGuest(t, ctx, binary, baseURL, projectRoot, "exec", sandboxID, "--command", curlCode+" https://example.com/ 2>&1")
	if !strings.Contains(afterEdit, "200") {
		t.Fatalf("after the edit the running sandbox was still refused: %q", afterEdit)
	}

	logs := daemon.logs.String()
	if !strings.Contains(logs, "egress proxy denied a request") {
		t.Fatalf("the daemon did not record the refusals:\n%s", logs)
	}
	if !strings.Contains(logs, "engine.blocked-address") {
		t.Fatalf("the metadata refusal did not name the engine rule:\n%s", logs)
	}
	if !strings.Contains(logs, "engine.blocked-port") {
		t.Fatalf("the control-plane port refusal did not name the engine rule:\n%s", logs)
	}
}

// networkPolicyE2EHostAddress returns the host address a sandbox can reach and
// the proxy is published over, which is the sandbox network's gateway for the
// default network a sandbox starts on.
func networkPolicyE2EHostAddress(t *testing.T, ctx context.Context) string {
	t.Helper()
	if configured := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_E2E_NETWORK_POLICY_HOST_ADDRESS")); configured != "" {
		return configured
	}
	dockerClient := newE2EDockerClient(t, ctx)
	network, err := dockerClient.NetworkInspect(ctx, "bridge", networkapi.InspectOptions{})
	if err != nil {
		t.Skipf("inspect the Docker bridge network: %v", err)
	}
	for _, config := range network.IPAM.Config {
		if gateway := strings.TrimSpace(config.Gateway); gateway != "" {
			return gateway
		}
	}
	t.Skip("the Docker bridge network exposes no gateway address; set AGENT_COMPOSE_E2E_NETWORK_POLICY_HOST_ADDRESS")
	return ""
}

// unusedNetworkPolicyAddress reserves a free port on the address sandboxes use to
// reach the daemon, so the proxy listener is reachable from inside a sandbox.
func unusedNetworkPolicyAddress(t *testing.T, host string) string {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatalf("allocate daemon listen address on %s: %v", host, err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("release daemon listen address: %v", err)
	}
	return address
}

// networkPolicyE2EFixture is a host-local HTTP endpoint the address rows name.
// Its address is outside the proxy's bypass list, so a request for it is the
// proxy's to allow or refuse.
type networkPolicyE2EFixture struct {
	host string
	port int
}

func (f networkPolicyE2EFixture) address() string {
	return net.JoinHostPort(f.host, strconv.Itoa(f.port))
}

// uncoveredAddress is the fixture address on a port no entry allows, so the
// declaration refuses it although it names the same address.
func (f networkPolicyE2EFixture) uncoveredAddress() string {
	return net.JoinHostPort(f.host, strconv.Itoa(f.port+1))
}

// startNetworkPolicyE2EFixture serves a fixed body on the address the address
// rows declare.
func startNetworkPolicyE2EFixture(t *testing.T, host string) networkPolicyE2EFixture {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Skipf("listen on the fixture address %s: %v", host, err)
	}
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("agent-compose network policy fixture\n"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("read the fixture address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("read the fixture port from %q: %v", portText, err)
	}
	return networkPolicyE2EFixture{host: host, port: port}
}

// runNetworkPolicyE2ECLI runs a CLI command that must succeed.
func runNetworkPolicyE2ECLI(t *testing.T, ctx context.Context, binary, baseURL, projectRoot string, args ...string) string {
	t.Helper()
	output, err := execNetworkPolicyE2ECLI(ctx, binary, baseURL, projectRoot, args...)
	if err != nil {
		t.Fatalf("agent-compose %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

// runNetworkPolicyE2EGuest runs a CLI command whose exit status reports the guest
// command's own failure, such as curl refusing a tunnelled connection.
func runNetworkPolicyE2EGuest(t *testing.T, ctx context.Context, binary, baseURL, projectRoot string, args ...string) string {
	t.Helper()
	output, _ := execNetworkPolicyE2ECLI(ctx, binary, baseURL, projectRoot, args...)
	return output
}

// networkPolicyE2EGuestResult returns the guest command's own first output line.
// A guest command that reports nothing, such as a grep with no match, still
// prints its result before the CLI reports the command's exit status.
func networkPolicyE2EGuestResult(t *testing.T, ctx context.Context, binary, baseURL, projectRoot string, args ...string) string {
	t.Helper()
	line, _, _ := strings.Cut(strings.TrimSpace(runNetworkPolicyE2EGuest(t, ctx, binary, baseURL, projectRoot, args...)), "\n")
	return strings.TrimSpace(line)
}

func execNetworkPolicyE2ECLI(ctx context.Context, binary, baseURL, projectRoot string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, append([]string{"--host", baseURL}, args...)...)
	command.Dir = projectRoot
	// The test daemon listens on TCP; an inherited socket path would send the
	// command to whatever daemon happens to own it.
	command.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_SOCKET": ""})
	output, err := command.CombinedOutput()
	return string(output), err
}

// networkPolicyE2ESandboxID keeps one sandbox running so the edit has something to
// reach.
func networkPolicyE2ESandboxID(t *testing.T, ctx context.Context, binary, baseURL, projectRoot string) string {
	t.Helper()
	output := runNetworkPolicyE2ECLI(t, ctx, binary, baseURL, projectRoot, "run", "live-update", "--keep-running", "--command", "true", "--json")
	var started struct {
		SandboxID string `json:"sandbox_id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &started); err != nil {
		t.Fatalf("decode the started sandbox from %q: %v", strings.TrimSpace(output), err)
	}
	if strings.TrimSpace(started.SandboxID) == "" {
		t.Fatalf("no sandbox id in %q", strings.TrimSpace(output))
	}
	t.Cleanup(func() {
		// `rm` refuses a running sandbox, so the test stops it first: leaving it
		// behind would keep a container (or a microVM) alive after the run.
		for _, verb := range []string{"stop", "rm"} {
			cleanup := exec.Command(binary, "--host", baseURL, "sandbox", verb, started.SandboxID)
			cleanup.Dir = projectRoot
			cleanup.Env = overrideE2EEnv(os.Environ(), map[string]string{"AGENT_COMPOSE_SOCKET": ""})
			_ = cleanup.Run()
		}
	})
	return started.SandboxID
}

// networkPolicyE2EProject is the project the matrix applies, parameterized by the
// driver under test and by the address the address rows declare.
type networkPolicyE2EProject struct {
	driver      string
	fixtureHost string
	fixturePort int
}

// write writes the project. The live-update agent starts without example.com
// allowed; the second write adds it, which is the edit the running sandbox must
// observe.
func (p networkPolicyE2EProject) write(t *testing.T, projectRoot string, liveUpdateAllowsExample bool) {
	t.Helper()
	liveUpdateAllow := p.allow("api.github.com", 443, nil)
	if liveUpdateAllowsExample {
		liveUpdateAllow += p.allow("example.com", 443, nil)
	}
	project := "name: netpol-e2e\nagents:\n" +
		p.agent("matrix", "        default: deny-all\n        allow:\n"+
			p.allow("api.github.com", 443, nil)) +
		p.agent("open", "") +
		p.agent("allow-list-only", "        default: allow-all\n        allow:\n"+
			p.allow("api.github.com", 443, nil)) +
		p.agent("deny-list", "        default: allow-all\n        deny:\n"+
			"          - host: example.com\n            port: 443\n") +
		p.agent("port-blocked", "        default: deny-all\n        allow:\n"+
			p.allow("api.github.com", 6443, nil)) +
		p.agent("address-literal", "        default: deny-all\n        allow:\n"+
			p.allow(p.fixtureHost, p.fixturePort, nil)) +
		p.agent("address-constrained", "        default: deny-all\n        allow:\n"+
			p.allow(p.fixtureHost, p.fixturePort, []string{p.fixtureHost + "/32"})) +
		p.agent("address-out-of-range", "        default: deny-all\n        allow:\n"+
			p.allow(p.fixtureHost, p.fixturePort, []string{"203.0.113.0/24"})) +
		p.agent("name-out-of-range", "        default: deny-all\n        allow:\n"+
			p.allow("api.github.com", 443, []string{"203.0.113.0/24"})) +
		p.agent("metadata-allowed", "        default: deny-all\n        allow:\n"+
			p.allow("169.254.169.254", 80, []string{"169.254.0.0/16"})) +
		p.agent("live-update", "        default: deny-all\n        allow:\n"+liveUpdateAllow)
	if err := os.WriteFile(filepath.Join(projectRoot, "agent-compose.yml"), []byte(project), 0o644); err != nil {
		t.Fatalf("write the network policy project: %v", err)
	}
}

// agent renders one agent. network is the body of the agent's policy.network
// block, and an empty value declares no policy at all.
func (p networkPolicyE2EProject) agent(name, network string) string {
	block := "  " + name + ":\n" +
		"    provider: codex\n" +
		"    system_prompt: |\n" +
		"      Answer with the single word requested and nothing else.\n"
	if network != "" {
		block += "    policy:\n      network:\n" + network
	}
	return block + "    driver:\n      " + p.driver + ": {}\n"
}

// allow renders one allow entry, with the address constraint when the row
// declares one.
func (p networkPolicyE2EProject) allow(host string, port int, allowedIPs []string) string {
	entry := fmt.Sprintf("          - host: %s\n            port: %d\n", host, port)
	if len(allowedIPs) > 0 {
		entry += "            allowed_ips:\n"
		for _, address := range allowedIPs {
			entry += "              - " + address + "\n"
		}
	}
	return entry
}

// networkPolicyE2EHTTPCode returns the status the guest's curl reported after the
// `code=` marker it is told to print, so a status the tool prints itself cannot
// be confused with the 403 curl names in an error message.
func networkPolicyE2EHTTPCode(t *testing.T, output string) string {
	t.Helper()
	_, rest, found := strings.Cut(output, "code=")
	if !found {
		t.Fatalf("the guest printed no HTTP status: %q", strings.TrimSpace(output))
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 3 {
		t.Fatalf("the guest printed a truncated HTTP status: %q", strings.TrimSpace(output))
	}
	if _, err := strconv.Atoi(rest[:3]); err != nil {
		t.Fatalf("the guest printed %q where an HTTP status was expected", strings.TrimSpace(output))
	}
	return rest[:3]
}

// networkPolicyE2ESuccessfulCode reports whether a status means the request was
// completed: a redirect is still a completed exchange the proxy allowed.
func networkPolicyE2ESuccessfulCode(status string) bool {
	return status == "200" || status == "301" || status == "302"
}

// TestE2EDockerDeclaredPolicyFailsClosedWithoutAReachableProxy pins the strict
// half of the same behavior: when the engine cannot put itself in the traffic
// path — here because the daemon listens only on loopback, which no sandbox can
// reach — a declared deny policy must refuse the start under
// `EGRESS_UNENFORCEABLE_POLICY=refuse` rather than run the agent with the
// unrestricted egress the declaration forbids.
func TestE2EDockerDeclaredPolicyFailsClosedWithoutAReachableProxy(t *testing.T) {
	image := strings.TrimSpace(os.Getenv(dockerNetworkPolicyE2EImageEnv))
	if image == "" {
		t.Skipf("set %s to a guest image with network access", dockerNetworkPolicyE2EImageEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	repoRoot := e2eRepoRoot(t)
	root, err := os.MkdirTemp(repoRoot, ".netpol-refuse-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dataRoot := filepath.Join(root, "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	newE2EDockerClient(t, ctx, image)
	binary := e2eDaemonBinary(t, ctx, repoRoot, root)
	address := unusedNetworkPolicyAddress(t, "127.0.0.1")
	baseURL := "http://" + address
	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("agent-compose-netpol-refuse-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	daemon := startE2EDaemonWithEnv(t, binary, repoRoot, dataRoot, address, image, map[string]string{
		"AGENT_COMPOSE_RUNTIME_BASE_URL": baseURL,
		"AGENT_COMPOSE_SOCKET":           socketPath,
		"EGRESS_UNENFORCEABLE_POLICY":    "refuse",
		"RUNTIME_DRIVER":                 "docker",
	})
	waitForE2EDaemon(t, ctx, daemon, baseURL)
	t.Cleanup(func() { t.Logf("fail-closed daemon log:\n%s", daemon.logs.String()) })

	project := networkPolicyE2EProject{driver: "docker", fixtureHost: "127.0.0.1", fixturePort: 1}
	project.write(t, projectRoot, false)
	runNetworkPolicyE2ECLI(t, ctx, binary, baseURL, projectRoot, "up")

	output, err := execNetworkPolicyE2ECLI(ctx, binary, baseURL, projectRoot, "run", "matrix", "--rm", "--command", "echo SHOULD-NEVER-RUN")
	if err == nil {
		t.Fatalf("a declaration the engine cannot enforce started anyway: %s", strings.TrimSpace(output))
	}
	if !strings.Contains(output, "cannot enforce the declared restricting network policy") {
		t.Fatalf("the refusal did not name the unenforceable declaration: %s", strings.TrimSpace(output))
	}
	// The refusal has to name the driver whose limit produced it, not just the
	// declaration: the design doc records that this test pins both, and a
	// refusal that stopped naming the driver would otherwise pass silently.
	if !strings.Contains(output, `driver "docker" cannot enforce the declared restricting network policy`) {
		t.Fatalf("the refusal did not name the driver that cannot enforce the declaration: %s", strings.TrimSpace(output))
	}
	if strings.Contains(output, "SHOULD-NEVER-RUN") {
		t.Fatalf("the refused declaration ran its command: %s", strings.TrimSpace(output))
	}
	t.Logf("refusal: %s", strings.TrimSpace(output))
}
