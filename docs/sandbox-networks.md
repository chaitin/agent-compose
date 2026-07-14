# Sandbox networks

Agent-compose connects sandboxes through TCP port mappings. Docker, BoxLite,
and Microsandbox consume the same normalized bindings, while each runtime uses
its own port-publishing implementation.

## Project configuration

Declare logical networks at the project level and attach agents by name:

```yaml
name: network-demo

networks:
  frontend: {}
  backend:
    driver: port_mapping

agents:
  api:
    networks: [frontend, backend]
    expose:
      - "8080"
      - target: 9090
        host_port: 19090
        protocol: tcp

  worker:
    networks: [backend]

  dashboard:
    ports:
      - "127.0.0.1:18080:8080"
```

`port_mapping` is currently the only supported network driver. When internal
networking is enabled, an agent without an explicit attachment joins the
implicit `default` network.

## `expose`

`expose` creates an internal listener that forwards to a service port inside
the target sandbox.

- `"8080"` forwards to sandbox port 8080 and allocates the listener port
  dynamically.
- The long form accepts `target`, optional `host_port`, and `protocol`.
- `host_port` fixes the listener port. A bind conflict fails sandbox startup;
  agent-compose does not silently select another port.
- Only TCP is currently supported.

The listener address is selected by the target runtime. It is not configured
per agent.

## `ports`

`ports` publishes a sandbox service with an explicit host address. Supported
short forms are:

```yaml
ports:
  - "8080"                       # dynamic host port -> sandbox 8080
  - "18080:8080"                # 127.0.0.1:18080 -> sandbox 8080
  - "0.0.0.0:18080:8080"        # all host interfaces -> sandbox 8080
```

The default host IP is `127.0.0.1`. Host addresses must currently be IPv4, and
only TCP is supported.

## Publish addresses

Internal `expose` listeners use two optional daemon settings:

```text
NETWORK_DOCKER_PUBLISH_ADDRESS
NETWORK_RUNTIME_PUBLISH_ADDRESS
```

Docker targets use the Docker address. BoxLite and Microsandbox targets use
the runtime address. If an address is omitted, agent-compose reads the IPv4
gateway of Docker's default `bridge` network.

A daemon running as a native process or in host network mode usually needs no
override. A daemon running in a regular Docker bridge network may need both
addresses configured so every source sandbox can route to the resulting
listener.

## Current boundary

Named networks record logical membership and annotate internal bindings. The
`port_mapping` driver does not create a Docker network, install firewall rules,
provide DNS names, or transparently rewrite `api:8080`. Sandboxes on different
logical networks may therefore still be able to reach the same underlying
listener.

Applications use the actual `host_ip:host_port` recorded in sandbox network
state. Service discovery and access-policy enforcement are outside the current
network driver.
