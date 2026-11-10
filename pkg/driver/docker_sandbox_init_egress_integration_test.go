package driver

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestDockerSandboxInitEgressAuditIntegration is opt-in: it needs a Docker
// daemon and a guest image with the egress-audit ac-init. It runs the real
// driver path with SANDBOX_INIT_EGRESS=audit, proves the workload inherited the
// filter, and proves init reported the workload's connect to the daemon.
func TestDockerSandboxInitEgressAuditIntegration(t *testing.T) {
	image := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE"))
	if image == "" {
		t.Skip("set AGENT_COMPOSE_TEST_DOCKER_INIT_IMAGE to a guest image with the egress-audit ac-init")
	}
	hostTmp := hostVisibleTestDir(t)
	runtime, sandbox := startDockerInitSandboxForTest(t, hostTmp, image, "audit")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	filters, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", "grep '^Seccomp_filters:' /proc/self/status"},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(read seccomp filters) error = %v", err)
	}
	filterCount, err := parseSeccompFilterCount(filters.Stdout)
	if err != nil {
		t.Fatalf("seccomp filter output = %q: %v", filters.Stdout, err)
	}
	if filterCount < 2 {
		t.Fatalf("Seccomp_filters = %d, want the container profile plus the init egress filter", filterCount)
	}

	connectCommand := `python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); s.listen(1); p=s.getsockname()[1]; c=socket.create_connection(("127.0.0.1",p),2); print("connect-ok",p); c.close(); s.close()'`
	connected, err := runtime.Exec(ctx, sandbox, VMState{}, ExecSpec{
		Command: "/bin/sh",
		Args:    []string{"-c", connectCommand},
		Cwd:     "/",
	})
	if err != nil {
		t.Fatalf("Exec(connect) error = %v", err)
	}
	fields := strings.Fields(connected.Stdout)
	if len(fields) != 2 || fields[0] != "connect-ok" {
		t.Fatalf("connect output = %q, want connect-ok and the destination port", connected.Stdout)
	}
	port, err := strconv.Atoi(strings.TrimSpace(fields[1]))
	if err != nil {
		t.Fatalf("connect port = %q: %v", fields[1], err)
	}

	dockerClient, err := runtime.newClient()
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	defer func() { _ = dockerClient.Close() }()
	info, ok, err := runtime.findContainer(ctx, dockerClient, sandbox, VMState{})
	if err != nil || !ok {
		t.Fatalf("findContainer() = %v, %v; want the running init container", ok, err)
	}
	logs, err := runtime.readContainerLogs(ctx, dockerClient, info.ID)
	if err != nil {
		t.Fatalf("read container logs: %v", err)
	}
	for _, want := range []string{
		"sandbox init egress audit",
		"sandbox_id=" + sandbox.Summary.ID,
		"family=inet",
		"host=127.0.0.1",
		fmt.Sprintf("port=%d", port),
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("container logs do not contain %q:\n%s", want, logs)
		}
	}
}

func parseSeccompFilterCount(output string) (int, error) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "Seccomp_filters:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Seccomp_filters:"))
		return strconv.Atoi(value)
	}
	return 0, fmt.Errorf("no Seccomp_filters line")
}
