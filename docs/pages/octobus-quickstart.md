# OctoBus Integration Quick Start

This guide walks you through connecting agent-compose to [OctoBus](https://github.com/chaitin/OctoBus) — a local gateway that exposes approved enterprise APIs, tools, and services to AI agents — and verifying that an agent can call a capability from inside its sandbox. It takes about 15 minutes.

For configuration field reference, see the [agent-compose.yml Manual](https://github.com/chaitin/agent-compose/blob/main/docs/pages/agent-compose-yaml-manual.md) (`octobus_servers`, `capset_ids`). For internal architecture, see the [OctoBus integration design](https://github.com/chaitin/agent-compose/blob/main/docs/design/octobus_integration.md).

## Concepts in one minute

| Concept | What it is | Who owns it |
| --- | --- | --- |
| **Capability Gateway** | The OctoBus connection configured in the agent-compose settings page (`addr` + `token`). | agent-compose daemon |
| **capset** | A named set of capabilities published by OctoBus, composed of `capset -> service -> instance -> method` bindings. | OctoBus |
| **`capset_ids`** | The agent field that declares which capsets its sandboxes may use. | Your `agent-compose.yml` |
| **capability proxy (capproxy)** | A gRPC proxy inside the daemon. Sandboxes never talk to OctoBus directly; capproxy checks authorization and forwards calls. | agent-compose daemon |
| **`CAP_GRPC_LISTEN`** | Daemon startup variable: where capproxy listens for guest gRPC calls. | Daemon deployment |
| **`CAP_GRPC_TARGET`** | Daemon startup variable: the capproxy address as reachable *from inside sandboxes*; injected into sandboxes as an env var. | Daemon deployment |
| **`CAP_TOKEN`** | A per-sandbox credential generated at sandbox creation; capproxy uses it to resolve which capsets a calling sandbox is allowed to use. | Injected automatically |

The data flow at runtime:

```text
guest agent ──gRPC──▶ capproxy (CAP_GRPC_TARGET) ──gRPC──▶ OctoBus daemon
```

The guest only ever sees `CAP_GRPC_TARGET` and `CAP_TOKEN`. The OctoBus address and token stay inside the daemon and never enter the sandbox.

## Prerequisites

- A running **agent-compose daemon** (Docker driver is enough; see the repository [Quick start](https://github.com/chaitin/agent-compose)).
- The **`agent-compose` CLI** connected to that daemon (`--host` or the default endpoint).
- **Docker** to run OctoBus (or Node.js 20+ to run it from npm).
- The **web UI** (`with-ui` profile) for the settings-page step. If you run daemon-only, configure the daemon-wide gateway through the Settings API instead.

## Step 1 — Start OctoBus

Run OctoBus on the same Docker network as the agent-compose daemon so the daemon can reach it by container name. The repository's default Compose deployment uses the network created by the root `docker-compose.yml`:

```bash
docker run -d --name octobus \
  --network agent-compose_default \
  -v octobus-data:/var/lib/octobus \
  ghcr.io/chaitin/octobus:latest
```

OctoBus listens on `0.0.0.0:9000` in the container. If you prefer running it directly on the host instead:

```bash
npx @chaitin-ai/octobus serve
```

In that case, note the address the daemon container can use to reach the host (for example `host.docker.internal` on Docker Desktop, or the host's LAN IP on Linux).

## Step 2 — Publish a capset from OctoBus

OctoBus ships a calculator example service. Use the OctoBus CLI inside the container (or your local `octobus` binary) to import it, create an instance, and expose its methods through a capset named `dev`:

```bash
# Import the example service (OctoBus repository checkout shown here).
octobus service import calculator ./examples/calculator-js

# Create and start an instance of it.
octobus instance create calculator-test \
  --service calculator \
  --config-json '{"label":"primary"}'

# Create the "dev" capset and expose the instance's methods in it.
octobus capset create dev --name DevAgent
octobus capset add-instance dev calculator-test

# Confirm the catalog exposes the calculator methods.
octobus catalog dev --all --json
```

If you run OctoBus in Docker, execute these with `docker exec -it octobus octobus <args>` against a checkout of the [OctoBus repository](https://github.com/chaitin/OctoBus). Any other service package works the same way — the only thing agent-compose needs later is the capset id (`dev`).

> Access tokens on the capset are optional. If you add one (`octobus capset add-token dev local --token-stdin`), record it for Step 3 — the daemon sends it as `Authorization: Bearer <token>` on every upstream call.

## Step 3 — Point agent-compose at OctoBus

Two independent things must both be configured:

1. **The Capability Gateway connection** (control plane: how the daemon reads capsets from OctoBus), and
2. **The capability proxy address pair** (data plane: how sandbox guests place calls).

### 3a. Capability Gateway (control plane)

Open the web UI, go to **Settings → Capability Gateway**, and set:

- **Address**: the OctoBus admin API address as reachable from the daemon container, e.g. `http://octobus:9000` (Docker network) or `http://host.docker.internal:9000` (OctoBus on the host).
- **Token**: the capset/daemon token if you configured one in Step 2; leave empty otherwise.

The settings page immediately probes `GET /admin/v1/status` on OctoBus and shows the connection status and the number of published capability sets. A green status with your `dev` capset listed means the control plane is wired correctly.

The token is stored by the daemon only: it is redacted on read-back, never written into sandbox metadata, never injected into guest env, and never logged.

### 3b. Capability proxy (data plane)

Capability calls from inside sandboxes go through capproxy, which is bound at **daemon startup** from two environment variables. Add them to the daemon service in your deployment (for the repository Compose stack, add to the `agent-compose` service environment in `docker-compose.yml` or an override file):

```yaml
services:
  agent-compose:
    environment:
      # Where capproxy listens inside the daemon container.
      CAP_GRPC_LISTEN: 0.0.0.0:7411
      # The same listener as reachable from sandbox containers.
      CAP_GRPC_TARGET: agent-compose:7411
```

| Variable | Meaning | Example |
| --- | --- | --- |
| `CAP_GRPC_LISTEN` | Listen address of the daemon-internal gRPC capability proxy. | `0.0.0.0:7411` |
| `CAP_GRPC_TARGET` | Guest-reachable address of that proxy; injected into sandboxes as env var `CAP_GRPC_TARGET`. | `agent-compose:7411` |

Both must be set when the daemon starts. If either is missing, the settings page still shows OctoBus as connected, but new sandboxes will not receive usable capability connection variables. **Restart the daemon after changing these values**, and create a *new* sandbox to pick them up — existing sandboxes keep the env they were created with.

The listen port only needs to be reachable from sandbox containers; do not publish it to the host unless you have a specific reason.

## Step 4 — Bind capsets in your project

Declare the capset in the agent's `capset_ids` in `agent-compose.yml`:

```yaml
name: octobus-demo

agents:
  coder:
    provider: claude
    image: chaitin/agent-compose-guest:latest
    workspace:
      provider: file
      path: .
    capset_ids:
      - dev
```

Validate and apply:

```bash
agent-compose config --quiet
agent-compose up
```

### Optional: project-scoped OctoBus servers

The unqualified `dev` entry uses the daemon-wide gateway from Step 3a. If different projects need different OctoBus deployments, declare named servers at the top level and qualify the capset id:

```yaml
octobus_servers:
  internal:
    url: http://octobus:9000
    token: ${OCTOBUS_INTERNAL_TOKEN}

agents:
  coder:
    capset_ids:
      - dev              # daemon-wide gateway
      - internal/dev     # project server "internal"
```

A qualified entry `internal/dev` routes through the project server named `internal`, while OctoBus still receives `dev` as the capset id. Mixing qualified and unqualified entries is fine, and declaring `octobus_servers` never changes how unqualified entries resolve. See the [agent-compose.yml Manual](https://github.com/chaitin/agent-compose/blob/main/docs/pages/agent-compose-yaml-manual.md) for the full routing matrix.

## Step 5 — Verify the sandbox injection

Create a sandbox (or let a scheduler run create one) and inspect what the agent sees:

```bash
agent-compose sandbox ls
agent-compose inspect <sandbox-id> --json
```

A sandbox created with `capset_ids: [dev]` has:

- Env var **`CAP_GRPC_TARGET`** — the capproxy address from Step 3b.
- Env var **`CAP_TOKEN`** — a per-sandbox credential (marked secret; it is an agent-compose-issued token, *not* the OctoBus token).
- A **`capset=dev`** tag recording the authorization.
- A capability guide rendered into the MPI catalog at `runtime/mpi/catalog.md` (guest path `/data/runtime/mpi/catalog.md`), listing each gRPC method with the `x-octobus-capset` / `x-octobus-instance` metadata to send. The agent runtime reads this catalog into the agent's system context automatically, so the agent knows its capabilities at startup without reading any file itself.

Injection is **best-effort by design**: if OctoBus is temporarily unreachable when the sandbox is created, the sandbox still starts; the failure is recorded as a sandbox event, and capability calls simply error at runtime until OctoBus is back.

## Step 6 — Call a capability

Now just ask the agent to use the calculator:

```bash
agent-compose run coder "Use the calculator capability to add 20 and 22, and tell me the result."
```

Under the hood, the agent:

1. Connects to `$CAP_GRPC_TARGET` (plaintext HTTP/2 gRPC).
2. Sends metadata `x-capability-sandbox-token: $CAP_TOKEN`, plus the per-method `x-octobus-capset: dev` and `x-octobus-instance: calculator-test` from the capability guide.
3. Uses gRPC server reflection with the same capset metadata to discover request/response schemas.

capproxy resolves the token to the sandbox, checks that `dev` is in its authorized capset set, injects the OctoBus token server-side, and forwards the call. The expected outcome is the agent answering `42`.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Settings shows "not configured" | Gateway address empty | Set the address in Settings → Capability Gateway |
| Settings shows connection error | Address unreachable from the daemon container, or wrong token | `docker exec agent-compose wget -qO- http://octobus:9000/admin/v1/status` to test reachability; re-check the token |
| Control plane green, but sandboxes have no `CAP_GRPC_TARGET` env | `CAP_GRPC_LISTEN` / `CAP_GRPC_TARGET` missing at daemon startup | Set both, restart the daemon, create a new sandbox |
| Agent can't reach the proxy | `CAP_GRPC_TARGET` is a host address the sandbox can't resolve | Use the daemon's container name (Compose network) as the host part of `CAP_GRPC_TARGET` |
| gRPC `FailedPrecondition` on a business call | Missing `x-octobus-instance` metadata | The agent must send the instance id from the capability guide; check `catalog.md` content |
| gRPC permission error mentioning capset | Sandbox called a capset outside its authorized set | Add the capset to the agent's `capset_ids` and create a new sandbox |
| Capability calls fail but everything looks configured | OctoBus down at call time | Check `octobus status`; capability errors at runtime never block the sandbox itself |

## Security notes

- The OctoBus token never leaves the daemon: it is not returned to the frontend in plaintext, not injected into guest env, not written into sandbox metadata, and not logged.
- `CAP_TOKEN` only proves "this caller is this sandbox" to capproxy. It cannot call OctoBus by itself.
- The capset set bound at sandbox creation is the isolation boundary enforced by capproxy; a guest can only choose instances *within* an authorized capset.
- Capabilities are exposed to guests over gRPC only. MCP / Connect RPC / REST endpoints of OctoBus are not proxied into sandboxes.
