# agent-compose project policy library example

Languages: English | [中文](README.zh-CN.md)

This example shows the project-level `policies` library: one policy block per
audience, selected by name from each agent, plus one agent that declares its
policy inline. It runs on the Docker runtime driver.

`config`, `up`, and `ls` exercise only the control plane and do not require a
model or API key.

## Prerequisites

- The `agent-compose` daemon is running.
- Docker is required only when starting a real agent sandbox.
- `agent-compose-guest:latest` exists locally before a real run.

## Compose file

```yaml
name: docker-policy-library

policies:
  locked-down:
    network:
      default: deny-all
      allow:
        - host: api.github.com
          port: 443
  retained-runtime:
    stopped_runtime_policy: retain

agents:
  reviewer:
    provider: codex
    image: agent-compose-guest:latest
    policy: locked-down
    driver:
      docker: {}

  builder:
    provider: codex
    image: agent-compose-guest:latest
    policy: retained-runtime
    driver:
      docker: {}

  adhoc:
    provider: codex
    image: agent-compose-guest:latest
    policy:
      network:
        default: allow-all
    driver:
      docker: {}
```

## How selection works

- `policy: <name>` selects a `policies` entry. The name is resolved while the
  project is normalized, so every agent carries its effective policy block.
- Repeating a library block inline normalizes to the same specification and the
  same spec hash. Moving a shared block into `policies` is behavior-neutral.
- An undefined name is an error. It is never treated as "no policy", because an
  undeclared policy means unrestricted egress.
- Every library entry is validated even when no agent selects it, and an entry
  cannot name another entry.
- The library is authoring convenience and is not part of the normalized
  specification: `config` shows each agent's effective `policy` block.

## The library and an agent's own policy

The library is a catalog; an agent's `policy` is a selection from it or a block
of its own. The two never compose:

| Agent declaration | Effective policy |
| --- | --- |
| `policy: locked-down` | Exactly the `locked-down` entry. |
| `policy: {…}` inline | Exactly the inline block. |
| no `policy` key | No policy: unrestricted egress, `remove` on stop. |

- One key, one shape: a name is a scalar and a block is a mapping, so an agent
  cannot state both. There is exactly one effective policy per agent.
- A library entry does not merge into an agent that declares its own block. It
  takes effect only where an agent names it, so `adhoc` above is unaffected by
  `locked-down`.
- Nothing is inherited. There is no project-wide default policy today: an agent
  that declares no `policy` has none, which is what an undeclared policy has
  always meant.
- "One policy for most agents, one exception" needs no composition rule: the
  exception writes its own block and the rest name the entry.

Merging the two would produce a third block that appears nowhere in the file,
and a reader could no longer tell what a sandbox enforces by reading one
declaration. If a shared default is wanted later, it should be an explicit key
with a stated rule (agent overrides the default, or the tighter of the two
wins), not an implicit merge.

## Validate and apply

From this directory:

```bash
agent-compose config
agent-compose up
agent-compose ls
```

From the repository root without installing the binary:

```bash
go run ./cmd/agent-compose --file examples/agent-compose/docker-policy-library/agent-compose.yml config
go run ./cmd/agent-compose --file examples/agent-compose/docker-policy-library/agent-compose.yml up
```

Representative normalized output: the `policies` library is resolved away, and
each agent carries the block it selected.

```yaml
name: docker-policy-library
agents:
    - name: adhoc
      enabled: true
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
      policy:
        stopped_runtime_policy: remove
        network:
            default: allow-all
    - name: builder
      enabled: true
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
      policy:
        stopped_runtime_policy: retain
    - name: reviewer
      enabled: true
      provider: codex
      image: agent-compose-guest:latest
      driver:
        name: docker
        docker: {}
      policy:
        stopped_runtime_policy: remove
        network:
            default: deny-all
            allow:
                - host: api.github.com
                  port: 443
```

## Docker driver and `default: deny-all`

`reviewer` declares `default: deny-all`. The Docker driver cannot install that
filter itself, but the daemon's forward proxy is in the sandbox's traffic path:
the sandbox carries the proxy environment, `api.github.com:443` is allowed, and
every other destination is refused. `config` and `up` still succeed without a
model or API key, and a real `run` of `reviewer` filters egress instead of
failing closed.

If the guest cannot reach the daemon through the proxy, the declaration is
reported as unenforced and the sandbox runs with wider egress, unless the
deployment sets `EGRESS_UNENFORCEABLE_POLICY=refuse`. To reach destinations the
declaration does not name, use `builder` or `adhoc`, or change `locked-down` to
`default: allow-all` (the declaration is still validated, hashed, and stored,
but it never denies traffic).

## Clean up

```bash
agent-compose down
```
