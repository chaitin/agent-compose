package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/chaitin/agent-compose/pkg/sandboxinit"
	containerapi "github.com/docker/docker/api/types/container"
)

// sandboxProxyEnvItems resolves the driver-boundary proxy environment at
// creation time. The result is used only for the supervisor; the workload
// container does not receive the proxy credential in supervisor mode.
func sandboxProxyEnvItems(ctx context.Context, sandbox *Sandbox) ([]SandboxEnvVar, error) {
	if sandbox == nil || sandbox.ProxyEnv == nil {
		return nil, nil
	}
	return sandbox.ProxyEnv(ctx)
}

// sandboxSupervisorProxyURL picks the forward proxy URL out of the resolved
// proxy environment.
func sandboxSupervisorProxyURL(items []SandboxEnvVar) string {
	for _, name := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		for _, item := range items {
			if item.Name == name && strings.TrimSpace(item.Value) != "" {
				return strings.TrimSpace(item.Value)
			}
		}
	}
	return ""
}

// supervisorHostResolver resolves one engine hostname. It is a parameter so
// the endpoint builder is testable without the network.
type supervisorHostResolver func(host string) ([]net.IP, error)

// buildSandboxSupervisorEndpoints derives the engine-owned destinations the
// supervisor connects directly, plus the DNS answers init needs because it can
// no longer resolve names itself.
func buildSandboxSupervisorEndpoints(proxyURL, telemetryURL string, resolve supervisorHostResolver) ([]string, string, error) {
	if resolve == nil {
		resolve = net.LookupIP
	}
	direct := make(map[string]struct{})
	hosts := make(map[string][]string)
	add := func(raw string) error {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Hostname() == "" {
			return fmt.Errorf("parse engine endpoint %q: %w", raw, err)
		}
		port := parsed.Port()
		if port == "" {
			if strings.EqualFold(parsed.Scheme, "https") {
				port = "443"
			} else {
				port = "80"
			}
		}
		if _, err := strconv.Atoi(port); err != nil {
			return fmt.Errorf("engine endpoint %q has an invalid port: %w", raw, err)
		}
		host := strings.ToLower(strings.Trim(parsed.Hostname(), "[]"))
		endpoint := net.JoinHostPort(host, port)
		direct[endpoint] = struct{}{}
		if net.ParseIP(host) != nil {
			return nil
		}
		addresses, err := resolve(host)
		if err != nil {
			return fmt.Errorf("resolve engine hostname %q: %w", host, err)
		}
		if len(addresses) == 0 {
			return fmt.Errorf("resolve engine hostname %q: no address", host)
		}
		values := make([]string, 0, len(addresses))
		for _, address := range addresses {
			if address == nil {
				continue
			}
			value := address.String()
			values = append(values, value)
			direct[net.JoinHostPort(value, port)] = struct{}{}
		}
		if len(values) == 0 {
			return fmt.Errorf("engine hostname %q resolved to no address", host)
		}
		hosts[host] = append(hosts[host], values...)
		return nil
	}
	if err := add(proxyURL); err != nil {
		return nil, "", err
	}
	if err := add(telemetryURL); err != nil {
		return nil, "", err
	}
	endpoints := make([]string, 0, len(direct))
	for endpoint := range direct {
		endpoints = append(endpoints, endpoint)
	}
	sort.Strings(endpoints)
	encoded := ""
	if len(hosts) > 0 {
		data, err := json.Marshal(hosts)
		if err != nil {
			return nil, "", fmt.Errorf("encode engine hostname answers: %w", err)
		}
		encoded = string(data)
	}
	return endpoints, encoded, nil
}

// sandboxSupervisorInitEnv is the non-secret init environment for supervisor
// mode. The relay token is not here: Docker exec inherits the container
// environment, so the token travels over the authenticated init control socket
// instead.
func sandboxSupervisorInitEnv(engineHostsJSON string) []string {
	env := []string{
		sandboxinit.EgressRelaySocketEnvVar + "=" + sandboxSupervisorGuestSocket,
	}
	if strings.TrimSpace(engineHostsJSON) != "" {
		env = append(env, sandboxinit.EgressEngineHostsEnvVar+"="+engineHostsJSON)
	}
	return env
}

// validateDockerSandboxSupervisorNetwork fails closed when a reused container
// is not on Docker's none network. Starting it would keep the external NIC the
// supervisor mode exists to remove.
func validateDockerSandboxSupervisorNetwork(containerInfo containerapi.InspectResponse) error {
	mode := ""
	if containerInfo.HostConfig != nil {
		mode = string(containerInfo.HostConfig.NetworkMode)
	}
	if mode != "none" {
		return fmt.Errorf("docker sandbox container %s uses network mode %q, but %s=enforce requires none; remove the sandbox and create it again", containerInfo.ID, mode, sandboxSupervisorEnvName)
	}
	return nil
}
