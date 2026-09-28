# Agent-Compose Helm Chart

This chart installs the Agent-Compose daemon and its Kubernetes runtime
permissions. The daemon is intentionally deployed as one replica because its
SQLite state is single-writer.

## Install

```bash
helm install agent-compose ./charts/agent-compose \
  --kube-context prod-cluster \
  --namespace team-a \
  --create-namespace
```

The Helm release namespace is used as the default sandbox namespace. The chart
also renders the in-cluster callback URL and the ClusterRoleBinding subject
from that namespace; no `agent-compose` namespace is hard-coded.

## Common values

```bash
helm upgrade --install agent-compose ./charts/agent-compose \
  --kube-context prod-cluster \
  --namespace team-a \
  --create-namespace \
  --set image.repository=registry.example.com/agent-compose \
  --set image.tag=v1.2.3 \
  --set guestImage=registry.example.com/agent-compose-guest:v1.2.3 \
  --set persistence.storageClass=fast-ssd \
  --set persistence.size=20Gi
```

Use `runtime.sandboxNamespace` only when the daemon is intentionally allowed
to create sandbox Pods in a namespace other than the release namespace. The
target namespace must exist and the chart's cluster-scoped RBAC must remain
enabled.

For private registries, create an image pull secret and pass it with
`--set imagePullSecrets[0].name=registry-credentials`.

## Daemon default LLM credential

Agents that supply their own credential via `agent-compose.yml` (an agent's
`env:` block, resolved by the `agent-compose` CLI from its own local
environment and sent per-session) need nothing from this chart. This section
is only for the daemon's *fallback* provider - the one it bootstraps from its
own process environment when a session supplies no credential of its own
(`LLM_API_KEY`/`OPENAI_API_KEY`/`LLM_MODEL`/`LLM_API_ENDPOINT`/
`LLM_API_PROTOCOL`/`LLM_MAX_OUTPUT_TOKENS`, all loaded in
`pkg/config/config.go`'s `loadLLMEnvConfig`). Locally this is the same `.env`
file `agent-compose daemon` loads via `godotenv.Load()` on startup
(`cmd/agent-compose/cli_daemon.go`) - `os.Getenv` does not care whether a
variable came from that file or from a Pod's env, so the same `.env` maps
directly onto the daemon's container env.

Import the whole `.env` as one Secret:

```bash
kubectl create secret generic agent-compose-env \
  --namespace team-a \
  --from-env-file=.env
```

```yaml
# values.yaml
extraEnvFrom:
  - secretRef:
      name: agent-compose-env
```

`--from-env-file` turns every `KEY=VALUE` line into its own Secret entry, and
`envFrom.secretRef` injects each one as a same-named container env var - the
raw values never pass through `--set`/values files/`helm get values` history,
only the Secret's name does. It's fine for this Secret to also carry
non-sensitive keys (`LLM_MODEL`, `LLM_API_ENDPOINT`, `LLM_API_PROTOCOL`) if
that's what's already in your `.env` - `kubectl create secret` doesn't care,
and Kubernetes lets an explicit `env:` entry win over an `envFrom`-sourced one
of the same name, so this chart's own managed `HTTP_LISTEN` and
`AGENT_COMPOSE_RUNTIME_BASE_URL` are never clobbered even if your local `.env`
also sets them (as the daemon dev `.env.example` does, for the local Docker
callback path) - only the LLM_* values from the file actually take effect.

For multiple providers or per-model metadata, use `$DATA_ROOT/models.json`
instead (see the [agent-compose.yml manual's "Daemon `models.json`"
section](../../docs/pages/agent-compose-yaml-manual.md#daemon-modelsjson)). The
chart has no dedicated field for it today - with `persistence.enabled: true`
(the default), `$DATA_ROOT` is the daemon's PVC (mounted at `/data`), so
`kubectl cp models.json <pod>:/data/models.json` and a rollout restart works;
there is no ConfigMap/extra-volume mount for it yet.

## Inspect and remove

```bash
helm get manifest agent-compose -n team-a
kubectl -n team-a rollout status deployment/agent-compose
helm uninstall agent-compose -n team-a
```

The chart marks its daemon PVC with `helm.sh/resource-policy: keep`, so
uninstalling the release leaves the PVC available for a later reinstall. Back
up daemon state before deleting the PVC manually.
