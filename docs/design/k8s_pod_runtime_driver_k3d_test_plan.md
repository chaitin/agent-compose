# K8s Runtime Driver — k3d Test Plan

Manual/exploratory test plan for the `k8s` runtime driver implemented per
[`k8s_pod_runtime_driver_design.md`](./k8s_pod_runtime_driver_design.md).
Uses [k3d](https://k3d.io) to stand up real local Kubernetes clusters
cheaply (k3s nodes as Docker containers) so the test exercises the actual
Kubernetes API, not a fake client.

## 0. What this validates

This plan was written before the storage/push redesign in design doc §2.1
and has been updated to match it: **the k8s driver mounts nothing** (no
`hostPath`, no k3d node volume binds needed at all - a real simplification
from the plan's original version), and sandbox data (prompts, skills, MCP
config) reaches the guest by an `Exec`-based push instead. Scenarios below
reflect the current implementation, not the original hostPath-based one.

| # | Scenario | Exercises |
|---|----------|-----------|
| 1 | Single cluster, no `driver.k8s` overrides | Basic Pod lifecycle: create → wait-running → find → exec → delete, **and** confirms the Pod declares no volumes and that a real prompt reaches the guest via push |
| 2 | `driver.k8s.namespace` override | `namespaceFor()` per-agent override vs. daemon `K8S_NAMESPACE` default |
| 3 | Two clusters, `driver.k8s.context` per agent | **The core regression target of the client-cache change**: `client()` cache-by-context (previously a single `sync.Once`, which would have silently pinned every agent to whichever cluster was resolved first) |
| 4 | Daemon restart mid-flight | `findPod()` reconciles the existing Pod (by name, then label selector) instead of creating a duplicate |
| 5 | Bad `driver.k8s.context` name | `client()` surfaces a clear error and doesn't poison the cache for other, valid contexts |
| 6 | Pod stuck `Pending` (bad image) | `waitForPodRunning()` times out (`SandboxStartTimeout`) with a clear error instead of hanging |
| 7 | Stop → re-run | Pod is actually deleted; the next run creates a fresh Pod rather than colliding with a stale name |
| 8 | Named volume declared for a k8s agent | Maps to a PVC and mounts into the Pod, while bind mounts and non-`k8s` volume drivers are rejected by `resolveProjectRunVolumeMounts` with a clear error |

Scenario 1's push-verification is the one that matters most now - it's the
direct functional test for design doc §6's write-call-site work (without it,
a k8s sandbox couldn't run an actual agent prompt at all). Scenario 3 remains
the regression test for the client-cache-by-context fix. If you only have
time for two scenarios, do those.

## 1. Prerequisites

- Docker Desktop (or another local Docker daemon) running — k3d runs k3s
  nodes as Docker containers.
- `k3d` (`brew install k3d`), `kubectl`, and `jq` (for the compiled-driver
  assertion below).
- This branch checked out; `buf` available. The generated Go/Connect sources
  (`proto/agentcompose/v2/agentcompose.pb.go` and
  `proto/agentcompose/v2/agentcomposev2connect/agentcompose.connect.go`) are
  tracked and committed, so they are present already; regenerate them with
  `task generate` (or `task generate:proto`) only if they are stale.

## 2. Build the daemon with k8s support

The stock `task build` on macOS uses the `darwin-docker` profile, which
**does not** include the `k8scompose` build tag (see
`scripts/build-agent-compose-binary.sh`: only `linux-full` does, and that
profile also pulls in boxlite/microsandbox cgo prep). For local k3d testing
that's unnecessary weight — build directly instead:

```bash
task generate:proto   # only if you haven't already regenerated the .pb.go
go build -tags k8scompose -o build/agent-compose ./cmd/agent-compose
export PATH="$PWD/build:$PATH"
```

This produces a native macOS binary with `k8s` in its compiled-driver list
(no cgo required — only `boxlitecgo`/`microsandboxcgo` need that). Confirm with
the actual machine-readable version command:

```bash
agent-compose --json version | jq -e '.compiled_drivers | index("k8s")'
```

The command must print a non-null index. `agent-compose version --drivers` is
not a supported CLI form.

## 3. Scenario 1 — single cluster smoke test

```bash
k3d cluster create agent-compose-dev
kubectl config use-context k3d-agent-compose-dev
kubectl create namespace agent-compose-test
```

No node volume bind needed — unlike the original version of this plan, the
k8s driver doesn't mount anything into the Pod at all (design doc §2.1); a
plain `k3d cluster create` is enough.

Start one daemon in a dedicated shell and keep it running for the scenarios
below:

```bash
RUNTIME_DRIVER=k8s K8S_NAMESPACE=agent-compose-test \
  AGENT_COMPOSE_RUNTIME_BASE_URL=http://host.docker.internal:7410 \
  HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon
```

(`K8S_KUBECONFIG`/`KUBECONFIG` deliberately left unset — k3d already merged
this cluster into `~/.kube/config`, and client-go's default loading rules
pick that up. This also exercises the "no explicit kubeconfig path"
fallback path in `client()`.)

`AGENT_COMPOSE_RUNTIME_BASE_URL=http://host.docker.internal:7410` is a
k3d-local dev shortcut, not either of design doc §2/§2.2's real deployment
paths: k3d nodes are Docker containers on the same Docker network as the
host, so `host.docker.internal` happens to resolve back to wherever this
daemon is running. Neither a real remote cluster (§2.2, needs Tailscale
Cluster Egress or equivalent) nor an in-cluster daemon (§2, uses its own
Service DNS instead) has this shortcut available - this is purely a way to
exercise the driver logic locally without setting up either.

`agent-compose down` stops the project schedulers and sandboxes; it does not
stop this daemon. Do not start a second daemon on port `7410` in a later
scenario. For the restart scenario, stop this one explicitly and start one
replacement after it has exited.

In another shell:

```bash
export AGENT_COMPOSE_HOST=http://127.0.0.1:7410   # or pass --host on every call
cat > agent-compose.yml <<'EOF'
name: k3d-smoke
agents:
  worker:
    provider: codex           # substitute whichever provider you have credentials for
    image: chaitin/agent-compose-guest:latest
    env:
      AGENT_COMPOSE_TEST: from-compose
    driver:
      k8s: {}
EOF
agent-compose up
agent-compose run worker --prompt "check the weather in guest state root" --keep-running
```

Verify, from the kubectl side:

```bash
kubectl -n agent-compose-test get pods -l agent-compose.driver=k8s
```

Expect one Pod, `Running`, labeled `agent-compose.sandbox_id=<id>`.

**Confirm the Pod declares no `hostPath`-style sandbox volumes** (design
doc §2.1 - this is the thing that changed since the plan's original
version):

```bash
kubectl -n agent-compose-test get pod <pod-name> -o jsonpath='{.spec.volumes}{"\n"}{.spec.containers[0].volumeMounts}{"\n"}'
```

Expect exactly one volume/mount: the `kube-api-access-*` projected
ServiceAccount token k8s injects into every Pod by default (not something
this driver declares). Anything else - a `HostPath` volume in particular -
means something regressed back toward the old design.

If your `provider` has no working credentials configured, the LLM call
itself may fail — that's fine for Pod-lifecycle purposes, since Pod creation
(`EnsureSandbox`) happens before the provider is invoked; the Pod should
still exist and be `Running`. It's *not* fine for validating the push
mechanism below, which happens before the provider call too but needs to be
checked independently of whether that call itself succeeds.

**Use `kubectl exec` directly to verify the push, not `agent-compose exec`.**
This was confirmed the hard way on the first live run: `agent-compose exec`
(the CLI subcommand) doesn't work against k8s sandboxes at all today -
unlike the driver's own low-level `Exec` used throughout this design
document, the `exec` CLI command goes through a separate, more
sophisticated file-based RPC mechanism
(`pkg/agentcompose/api/exec_execution.go`, an in-guest Node.js runtime
helper) that assumes the shared mount docker, boxlite, and microsandbox
all have and fails
with `ENOENT ... command-request.json` on k8s (see design doc §4.3 - a
real, separate gap, not fixed yet). `kubectl exec` bypasses the daemon and
this mechanism entirely, so it works fine for verification purposes:

```bash
kubectl -n agent-compose-test exec <pod-name> -- sh -c \
  'cat "$(ls -t /data/state/agents/prompts/*.txt | head -1)"'
```

Expect the exact prompt text (`check the weather in guest state root`)
back - this is the direct test for design doc §6's push work; without it,
this would return nothing, since nothing mounts `/data` into the Pod.
Then confirm env vars from the compose file and the pushed codex MCP/
runtime config both made it too:

```bash
kubectl -n agent-compose-test exec <pod-name> -- sh -c 'echo AGENT_COMPOSE_TEST=$AGENT_COMPOSE_TEST'
kubectl -n agent-compose-test exec <pod-name> -- sh -c 'head -5 /root/.codex/config.toml'
```

Expect `AGENT_COMPOSE_TEST=from-compose` and real codex config content
(`model_provider = ...`, `[model_providers.agent_compose]`, etc.) - the
latter confirms `llms.WriteCodexMCPConfig`'s push (design doc §6) is
working, not just the prompt.

If you have working provider credentials configured, you don't need any of
the manual verification above - `agent-compose run` itself completing with
a real model response (not an error) is direct proof the prompt reached the
guest and codex could read it. This was the actual outcome on the first
live run: the response text engaged with the literal prompt content
("Which location do you mean by 'guest state root'? ..."), showing codex
genuinely received it.

Clean up:

```bash
agent-compose down
kubectl -n agent-compose-test get pods   # expect empty
```

## 4. Scenario 2 — namespace override

Add a second agent with an explicit namespace, and pre-create that
namespace (agent-compose does not create namespaces, only Pods):

```bash
kubectl create namespace agent-compose-custom
```

```yaml
agents:
  worker:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s: {}
  worker-custom-ns:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s:
        namespace: agent-compose-custom
```

```bash
agent-compose up
agent-compose run worker-custom-ns --prompt "echo hello" --keep-running
kubectl -n agent-compose-custom get pods       # expect the new Pod here
kubectl -n agent-compose-test get pods          # expect nothing new here
```

## 5. Scenario 3 — two clusters, per-agent context (the regression test)

```bash
k3d cluster create agent-compose-a
k3d cluster create agent-compose-b
kubectl config get-contexts   # confirm k3d-agent-compose-a and k3d-agent-compose-b both exist
kubectl --context k3d-agent-compose-a create namespace agent-compose-test
kubectl --context k3d-agent-compose-b create namespace agent-compose-test
```

```yaml
agents:
  worker-a:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s:
        context: k3d-agent-compose-a
        namespace: agent-compose-test
  worker-b:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s:
        context: k3d-agent-compose-b
        namespace: agent-compose-test
```

```bash
# Reuse the daemon started in scenario 1. If it was stopped, start exactly one
# replacement and wait for /api/version before continuing.
agent-compose up
agent-compose run worker-a --prompt "echo hello" --keep-running
agent-compose run worker-b --prompt "echo hello" --keep-running
```

Verify each Pod landed in its own cluster, not both in whichever cluster
was resolved first. Record each run's sandbox ID from `agent-compose ps`; the
IDs are needed by the restart scenario:

```bash
kubectl --context k3d-agent-compose-a -n agent-compose-test get pods   # expect worker-a's Pod only
kubectl --context k3d-agent-compose-b -n agent-compose-test get pods   # expect worker-b's Pod only
```

**This is the scenario that would have failed before the `client()`
refactor**: with the old `sync.Once`-cached single client, `worker-b`'s Pod
would have silently been created in cluster A (or the create would fail
with an auth error, depending on which context's credentials happened to
be cached first) — either way, not in cluster B where `driver.k8s.context`
asked for it.

## 6. Scenario 4 — daemon restart reconciliation

With `worker-a`'s Pod and sandbox still running from scenario 3, record its
sandbox ID as `<worker-a-sandbox-id>`:

```bash
# In the daemon shell: press Ctrl-C (or kill the daemon) and wait for it to exit.
# Then, in a fresh shell, start exactly one replacement:
RUNTIME_DRIVER=k8s HTTP_LISTEN=127.0.0.1:7410 agent-compose daemon &
until curl -fsS http://127.0.0.1:7410/api/version >/dev/null; do sleep 1; done
agent-compose run --sandbox <worker-a-sandbox-id> worker-a \
  --prompt "echo hello again" --keep-running
kubectl --context k3d-agent-compose-a -n agent-compose-test get pods -l agent-compose.sandbox_id
```

Expect **the same Pod** (same name/age), not a second one — `findPod()`
should locate it by name/label before `EnsureSandbox` falls through to
`createPod`. Without `--sandbox <worker-a-sandbox-id>`, `run` creates a new
sandbox and this scenario does not test restart reconciliation.

## 7. Scenario 5 — bad context name

```yaml
  worker-bad-context:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s:
        context: does-not-exist
```

```bash
agent-compose run worker-bad-context --prompt "echo hello" --keep-running
```

Expect a clear error surfaced to the CLI (from `client()`'s wrapped error:
`load kubernetes client config for context "does-not-exist": ...`), not a
hang or a silent fallback to some other cluster. Then confirm `worker-a`
and `worker-b` still work normally afterward (the failed context must not
have corrupted the client cache for the others).

## 8. Scenario 6 — stuck Pending pod (timeout)

```yaml
  worker-bad-image:
    provider: codex
    image: registry.invalid/does-not-exist:latest
    driver:
      k8s: {}
```

```bash
agent-compose run worker-bad-image --prompt "echo hello" --keep-running
```

Expect the run to fail once `SandboxStartTimeout` elapses (default 30 minutes
if unset). For a fast manual check, stop and restart the daemon before this
scenario with `SANDBOX_START_TIMEOUT=10s` in its environment. While the run is
waiting, inspect the Pod with:

```bash
kubectl -n agent-compose-test get pod -o wide
kubectl -n agent-compose-test get pod <bad-image-pod> -o jsonpath='{.status.phase}{"\n"}{.status.containerStatuses[0].state.waiting.reason}{"\n"}'
```

Expect `phase=Pending` and usually `reason=ImagePullBackOff` (the latter is a
container waiting reason, not a Pod phase). The current implementation's final
CLI error is a timeout naming the Pod; it does not include the last observed
phase/reason. A newly created sandbox may be cleaned up after the run fails,
so capture this state before the timeout expires.

## 9. Scenario 7 — stop and re-run

```bash
agent-compose ps                              # note worker's sandbox id
agent-compose sandbox rm <sandbox-id> --force
kubectl -n agent-compose-test get pods        # expect the Pod gone
agent-compose run worker --prompt "echo hello once more" --keep-running
kubectl -n agent-compose-test get pods        # expect a fresh Pod, not an error about an existing name
```

## 10. Scenario 8 — named volume mapped to a PVC, bind mount rejected

```yaml
name: k3d-volume
volumes:
  cache:
    driver: k8s
agents:
  worker-with-volume:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    driver:
      k8s: {}
    volumes:
      - type: volume
        source: cache
        target: /cache
```

```bash
agent-compose up
kubectl -n agent-compose-test get pvc          # expect the cache PVC
agent-compose run worker-with-volume --prompt "echo hello" --keep-running
kubectl -n agent-compose-test get pod <pod-name> -o jsonpath='{.spec.volumes}{"\n"}'
```

Named volumes are the **supported** k8s mount path: `driver: k8s` selects the
PVC-backed volume driver (`pkg/volumes/k8s_driver.go`), and the Pod gets a
`PersistentVolumeClaim` volume for it (`pkg/driver/k8s_runtime.go`'s
`podVolumeSpecs`). Expect the PVC to exist and the Pod to mount it at `/cache`.
Defaults are `size: 1Gi` and `access_mode: ReadWriteOnce`, overridable together
with `storage_class` and `namespace` under the volume's `options`; the PVC and
the Pod must share a namespace or sandbox creation fails. This is the design doc
§2.3 "named volumes map to PVCs" validation.

Then confirm the two unsupported forms are rejected before any Pod is created by
`resolveProjectRunVolumeMounts` (`pkg/runs/sandbox_preparation.go`), which
delegates to `pkg/volumes/normalize.go` - not silently dropped or mis-mounted:

- `type: bind` is rejected while normalizing an agent that declares a `k8s`
  driver (`pkg/compose/normalize.go`, path `agents.<name>.volumes[i].type`) with
  `k8s driver does not support local bind mounts; use a named volume instead`.
  The resolved-volume path (`pkg/volumes/normalize.go`, reached through
  `resolveProjectRunVolumeMounts`) reports the same rejection with the source and
  target included - a bind source is a daemon-host path that has no meaning
  inside a Pod.
- A named volume whose driver is not `k8s` (for example the `local` default)
  fails with `k8s driver does not support volume driver "<driver>" for source
  "..."; use a volume with driver k8s`.

## 11. Cleanup

```bash
agent-compose down
k3d cluster delete agent-compose-dev agent-compose-a agent-compose-b
```

## 12. What's still not covered by this plan

- **`WriteAgentSkills`/MCP config push** (design doc §6) - scenario 1 only
  exercises the prompt push. To exercise skills/MCP config too, add
  `skills:`/`mcp_servers:` to the `worker` agent definition and, after
  `run`, `agent-compose exec` a `cat`/`ls` against
  `/root/.agents/skills`, `/root/.claude/skills`, `/data/state/agents/mcp/config.json`,
  or `/root/.codex/config.toml` (paths per `pkg/execution/agent_files.go`/
  `pkg/llms/runtime_config.go`) instead of the prompt path shown above.
- **Large workspace push** - the stdin-based streaming tar path has unit
  coverage above the former 512 KiB argv limit. A live multi-megabyte
  workspace is still useful E2E coverage for API-server and network behavior.
- **Session resume** (`CollectAgentResumeInfo`'s guest-pull path, design
  doc §6) - needs two real runs of the same agent/sandbox with working
  provider credentials to observe a thread ID being resolved from the
  guest-pulled state file on the second run; not practical to script
  without a real provider account.
- **The harvest-before-delete pull and the other `Read`/`WriteGuestDir`
  call sites (cell/exec artifacts)** - still unimplemented per design doc
  §6, so nothing to validate here yet.
- The guest image must be pullable by the k3d node's container runtime. A
  Docker image that exists only in the host Docker image store is not
  automatically available to k3d. For a locally built image, import it
  explicitly:

  ```bash
  k3d image import my-image:dev -c agent-compose-dev
  ```

The guest image must be pullable by the k3d node's container runtime. A Docker
image that exists only in the host Docker image store is not automatically
available to k3d. For a locally built image, import it explicitly:

```bash
k3d image import my-image:dev -c agent-compose-dev
```

## Notes / things to double-check while running this

- Substitute a `provider`/credentials setup you actually have available;
  the scenarios above only depend on the LLM call itself for the parts
  explicitly marked as needing a successful prompt (none of the Pod-lifecycle
  assertions do).
- **Scenario 1 has actually been run end-to-end** (2026-08-25, against a
  real k3d cluster with working codex credentials) - it's what found and
  fixed the two bugs documented in design doc §5.1, and is where the note
  above about `agent-compose exec` not working on k8s came from. Scenarios
  2-8 are still design-reviewed but not run yet.
- CLI surface used above (`run --keep-running`, `down` taking no args,
  `sandbox stop|rm <sandbox-id>`, `ps`) was confirmed both against the
  actual command definitions in `cmd/agent-compose/cli_*.go` and, for
  scenario 1, by actually running it. `agent-compose exec` is confirmed
  *not* to work against k8s sandboxes (see above) - use `kubectl exec`
  instead for any verification step that needs to reach inside the Pod.
- `agent-compose.yml` accumulates agents across scenarios in the examples
  above for brevity; split into separate files/projects if you'd rather
  keep scenarios isolated.
