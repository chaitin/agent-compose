# Engine capability matrix

`EngineService.GetCapabilities` answers "what can this engine enforce, and by
what mechanism". It exists so an isolation requirement check (SEC-3) can decide
whether a declared requirement is supported using only this RPC.

`EngineService` is not `CapabilityService`. `CapabilityService` is the
capability-gateway (capset/catalog) surface; `EngineService` reports the
engine's own isolation and runtime capability matrix. The owning Go package is
`pkg/capmatrix`, not `pkg/capability` or `pkg/capabilities`.

## Capability shape

Each capability dimension returns:

- `dimension`: a stable string key;
- `enforced`: whether the engine actively imposes the dimension;
- `mechanism`: when enforced, the concrete mechanism; when not enforced, one of
  the closed reasons `unsupported` (the driver has no configuration surface) or
  `not_configured` (a surface exists but the engine does not set it);
- `preconditions`: conditions that must hold for the mechanism to take effect;
- `observed`: what the driver actually writes into its runtime configuration;
- `default_behavior`: what happens when the declaration is silent.

**Invariant:** `enforced = true` with an empty `mechanism` is invalid.
`capmatrix.BuildSnapshot` / `NewSnapshot` reject such a declaration at startup,
`Snapshot.Validate()` re-checks at the transport boundary, and the RPC never
returns it. A capability that is not enforced must carry a closed reason rather
than free text.

## Dimensions

| Dimension | Meaning |
| --- | --- |
| `resource_limits` | CPU, memory, and disk limits written into the runtime configuration |
| `security_context.capability_drop` | dropping Linux capabilities |
| `security_context.read_only_rootfs` | read-only workload root filesystem |
| `security_context.non_root_user` | running the workload as a non-root user |
| `security_context.user_namespaces` | user-namespace remapping |
| `egress_policy` | strength of outbound network restriction |
| `credential_placeholder_injection` | opaque placeholder instead of a real credential |
| `stopped_runtime_retention` | stopping a sandbox while preserving its writable runtime |
| `checkpoint_restore` | checkpointing and restoring a sandbox |
| `gpu_and_devices` | host GPU or device exposure |

Every driver declaration must cover all of them exactly once; the snapshot
validation rejects a driver that omits or duplicates a dimension.

`credential_placeholder_injection` is enforced by the engine, not by a driver:
`pkg/llms` strips the declared LLM provider credential and endpoint names from
the persisted and execution environments and installs a run-scoped facade token
and facade URL in their place, so the declared credential never reaches the
sandbox on any driver. The recognized-name scope is reported as a precondition,
because a declared secret whose name the daemon does not recognize as an LLM
provider credential still reaches the guest verbatim. A driver's own SDK secret
surface (BoxLite `boxlite_options_add_secret`, Microsandbox `WithSecrets`) is a
separate mechanism the engine does not bind, and it is recorded in `observed`
rather than in `enforced`.

## Reason classification

`unsupported` means the runtime or SDK the driver uses has no addressable
configuration surface for that exact dimension. `not_configured` means such a
surface exists and the engine does not set it. The distinction is what lets a
requirement check tell "this driver cannot" from "this engine does not today",
so the reason is tracked per dimension, not per driver: for example, the
Microsandbox SDK exposes `WithUser`, `SandboxHandle.Snapshot`, and
`RestoreSandbox`, so `security_context.non_root_user` and `checkpoint_restore`
on that driver are `not_configured`, while BoxLite's C surface exposes no
checkpoint call, so its `checkpoint_restore` is `unsupported`.

## Startup snapshot, no probing

Driver capabilities are static declarations in `pkg/driver/*_capabilities*.go`.
The composition root builds the snapshot once when routes are composed and
injects it into the handler constructor. A query never contacts a Docker
daemon, a KVM host, or a Kubernetes cluster, so it succeeds while every runtime
is unreachable. `captured_at` records when the snapshot was taken.

Declarations are cross-asserted against implementation in tests: Docker against
the real `HostConfig`, Kubernetes against the created Pod and the absence of a
NetworkPolicy, `credential_placeholder_injection` against
`llms.MergeManagedExecEnv` / `llms.FilterPersistedRuntimeEnv`, and the provider
feature matrix against the guest runner sources in
`runtime/javascript/src/runners`. The microVM resource declaration is asserted
against the shared `configuredSandboxResources` config struct that both drivers
read; the SDK calls that consume it are cgo- and KVM-gated, so they are
exercised by the opt-in runtime smoke path rather than by the unit tests.
`stopped_runtime_retention` is derived from
`RuntimeDriverSupportsStoppedRuntimeRetention` so report and behavior cannot
drift.

## `compiled_drivers` versus the capability snapshot

`compiled_drivers` (in `/api/version` and this response) reports what was
compiled into the binary. It does **not** check Docker daemon reachability, KVM
access, runtime artifact health, or whether a driver can start. Its semantics
are unchanged by this RPC.

The capability snapshot reports what the engine enforces and by what
mechanism. The two are complementary, not substitutes. The response repeats the
`compiled_drivers` list next to the snapshot and includes
`compiled_drivers_note`, which states the distinction so a consumer never has
to infer it from the field name.

## Provider capabilities

Each provider (`codex`, `claude`, `opencode`, `pi`, `dsh`) reports:

- `preferred_protocols`: the upstream protocol order, read from
  `llms.Dialect.PreferredProtocols()` so the RPC and the runtime resolution path
  cannot disagree;
- `features`: `structured_output`, `session_resume`, `streaming`, and
  `skill_injection`, matching the guest runner behavior in
  `runtime/javascript/src/runners`.
