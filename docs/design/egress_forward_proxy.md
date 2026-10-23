# Egress forward proxy

## Problem

A project can declare `policy.network`, but on three of four drivers the engine
installs no filter at all: Docker's only network knob is the daemon's network
mode, Kubernetes emits no egress `NetworkPolicy`, and BoxLite's host-side binding
is unverified. Until now the gap was papered over by refusing to start a sandbox
that declared `default: deny-all`, which turned a restriction into an availability
failure. The interim behavior is now `warn`: the sandbox runs and the daemon
reports the declaration as unenforced (`EGRESS_UNENFORCEABLE_POLICY=refuse` keeps
the fail-closed mode). That makes the gap honest, not smaller. This document
closes it.

## Decision: enforce in an engine-side forward proxy

The engine already terminates guest traffic: the runtime LLM facade and the
capset gateway are HTTP services the sandbox reaches inside the daemon. A forward
proxy on that same boundary is the enforcement point because it is the only place
that is simultaneously

- available on every driver whose guest can reach the daemon (Docker, Kubernetes,
  BoxLite, Microsandbox alike), so the declaration stops depending on driver
  features;
- authoritative for policy: it can read the sandbox's *current* declaration per
  request, so an edit changes enforcement for a running sandbox instead of only
  new ones;
- where the compiled `egress.Policy` already lives, with a content-derived
  generation, per-decision `Record`s and engine-endpoint auto-allow rules;
- where request-level (L7) enforcement and credential rewriting belong later.

The industry shape is the same one: a sandbox proxy that answers a disallowed
`CONNECT` with `403` and reloads rules while the sandbox runs, with policy DNS and
transparent TCP capture as later milestones rather than the first one.

## Shape

1. **Listener and dispatch.** The proxy is served on the daemon's existing HTTP
   listener. A request whose target is an absolute URI, or whose method is
   `CONNECT`, is dispatched to the proxy; everything else keeps the current
   handler. No new port and no new topology: the sandbox already reaches this
   listener, which is what `GuestRuntimeBaseURL` names.
2. **Sandbox identity.** The proxy URL carries a per-sandbox credential —
   `http://<sandbox-id>:<token>@<host>:<port>` — minted and revoked with the
   sandbox, the way the runtime LLM facade token already is. A request without a
   valid credential is refused, and the sandbox the credential resolves to is the
   only declaration consulted. The proxy never trusts a caller-supplied project
   or policy.
3. **Decision.** Every connection is decided by `egress.Decide` against the policy
   compiled from that sandbox's current declaration, with the engine's own
   endpoints allowed ahead of the declared entries as engine rules
   (`engine.llm-facade`, `engine.telemetry`, and the proxy's own endpoint). A deny
   answers `403` with the rule id and reason; an allow opens the tunnel to the
   requested host and port.
4. **DNS lives at the proxy.** The proxy resolves the destination, so the guest
   never resolves it and a rebinding answer cannot move the connection: the name
   is what policy approves, the address the proxy dialled is what it reports.
   Pinning the resolved address to declared `allowed_ips` ranges is the schema
   change that follows.
5. **Guest configuration.** The engine adds `HTTP_PROXY`, `HTTPS_PROXY` and
   `NO_PROXY` (engine endpoints excluded) to the sandbox runtime environment, not
   overridable by project env. That is what routes a well-behaved client through
   the decision above.
6. **Honest strength.** With the proxy enabled and reachable, a driver's egress
   strength becomes real (`strength=allowlist`, `mechanism=forward_proxy`) and the
   capability report says so, including the limit that a process which ignores the
   proxy environment bypasses it until transparent capture lands. Where the proxy
   is disabled or unreachable the report stays `strength=none` and the warn/refuse
   behavior above still applies — the engine never claims a limit it does not
   install.

## What this unlocks

- `default: deny-all` runs *and* filters on the default driver, which removes the
  reason for the refusal path entirely on Docker and makes the execution matrix
  testable without KVM.
- Policy becomes live: because enforcement reads the current declaration per
  request, the daemon-owned policy record and its query/modify RPC become a
  matter of storing what the proxy already reads, not of recreating sandboxes.

## Not in the first slice

Transparent TCP capture and policy DNS (which is what makes a
proxy-environment-ignoring process obey), request-level L7 rules, per-binary
scoping, and credential rewriting at the proxy. Each is a separate step on the
same boundary, and none of them changes the decision path above.

## Delivered, and what it changed

The first slice is implemented and verified on the Docker driver:

- `pkg/agentcompose/proxy` serves the proxy and decides every request;
  `SandboxEgressProxy` (`pkg/agentcompose/adapters`) issues the credential, builds
  the environment, answers the proxy's authentication, and reads the policy;
  `driverRuntimeAdapter.driverSandbox` is the single seam that attaches the
  `MediatedByForwardProxy` fact and installs the create-time provider, which the
  driver resolves when it builds a newly created runtime's environment. Resolving
  there rather than when the sandbox is handed to the driver keeps issuance to one
  credential per runtime creation, since a guest reads its environment once; a
  driver that reuses a runtime never calls the provider. `HTTP_LISTEN` serves the
  proxy.
- The engine's own refusals were extended past the address set: the control-plane
  ports `2379`, `2380`, `6443`, `10250`, `10255` are refused under their own rule
  with the role named. IPv4 and IPv6 answers are both carried through the
  canonical endpoint identifier and dialed; a CIDR host matches direct addresses
  and, for a name, is checked against every resolved answer for an allowance and
  any resolved answer for a denial.
- `allow[].allowed_ips` was added with it, because a name without an address
  constraint is exactly the indirection the proxy would otherwise make
  trustworthy. The constraint travels in the normalized spec, the proto messages
  and the driver-boundary policy, and a mechanism that cannot apply it must now
  say so rather than apply the destinations alone.
- `EGRESS_UNENFORCEABLE_POLICY` defaults to `warn`, so a declaration the platform
  cannot deliver is reported instead of becoming an availability failure.
- **Admission says which mechanism will apply.** `up`/`apply` report, per declaring
  agent, the declaration and the mechanism the creation seam will install
  (`driver.ReportAgentNetworkPolicy`, carried in `ApplyProjectResponse` and
  `ValidateProjectResponse`); `config` prints the declared half locally, because
  it never contacts the daemon. The verdict uses the same mediation predicate the
  seam uses (`SandboxEgressProxy.MediatesDriver`), so a report cannot claim a
  mechanism the creation path would not set, and the daemon fails the request
  rather than describe a project whose policy it cannot compile.

Evidence: `verify-forward-proxy.sh` (9/9) and `verify-allowed-ips.sh` (5/5) on the
Docker driver, plus `verify-warn-default.sh` (13/13) for the fallback.

## Verified on real drivers

The matrix below was executed against real sandboxes on this checkout. Every row
is one guest request decided against a declaration; `docker` and `microsandbox`
passed all of them, and `boxlite` could not be covered because its VM does not
boot in this environment. The row set is
`TestE2EDockerNetworkPolicyExecutionMatrix` in
`test/e2e/docker_network_policy_host_daemon_test.go`, which is opt-in: it needs a
guest image with `curl`, and the rows that name an address need a host-local
fixture on an address the proxy's bypass list does not cover.

| # | Declaration under test | Guest assertion | docker | microsandbox | boxlite |
| --- | --- | --- | --- | --- | --- |
| 1 | no `policy` block | reaches a public host and an arbitrary host address, and carries 0 `*_proxy` variables | pass | pass | not covered |
| 2 | `default: allow-all` with an allow list | the unlisted public host still reaches 200 | pass | pass | not covered |
| 2a | `default: allow-all` with a denial | the denied host is refused, an unlisted host still reaches 200 | not in this run | not in this run | not covered |
| 3 | `default: deny-all` + allow `api.github.com:443` | allowed host 200, `example.com` refused | pass | pass | not covered |
| 4 | the same declaration on port 6443 | refused under the engine's control-plane port rule | pass | pass | not covered |
| 5 | `default: deny-all` + allow an address literal | the literal reaches the fixture; an uncovered port on it is refused | pass | pass | not covered |
| 6 | the same entry with `allowed_ips` covering the address | the fixture is allowed | pass | pass | not covered |
| 7 | the same entry with `allowed_ips` that cannot cover it, by literal and by name | both refused, each naming the range | pass | pass | not covered |
| 8 | `default: deny-all` + an allow entry for the metadata address | still refused under the engine's address rule | pass | pass | not covered |
| 9 | an edit that adds an allowance to a running sandbox | the running sandbox observes it per request | pass | pass | not covered |
| 10 | any refused row | the daemon records the denial and the rule that produced it | pass | pass for the proxy-mediated rows; the SDK mechanism's own refusals are silent | not covered |

The denylist row is part of the execution matrix
(`TestE2EDockerNetworkPolicyExecutionMatrix`), but the run recorded above predates
it, so this table does not yet claim it for a real driver. The declaration
compiles to the same decision model, with denials placed ahead of allowances and
engine endpoints ahead of both, and is covered by unit tests and by the
forward-proxy path until that row is executed.

Mechanism per driver:

| Driver | What enforces a declaration | Evidence |
| --- | --- | --- |
| docker | the engine's forward proxy (`strength=allowlist`, `mechanism=forward_proxy`, applies `allowed_ips`) | full matrix pass in 139s, plus a 2.5s fail-closed case |
| microsandbox | the SDK `NetworkConfig`, and the engine's forward proxy for the traffic that uses it | full matrix pass in 85s, plus the mechanism-only run below |
| boxlite | the engine's forward proxy when the guest can reach the daemon; the driver installs no filter of its own | **not covered**: the VM does not boot here (`The VM failed to start`, `libkrun status=-2`, `Couldn't find or load libkrunfw.so.5` in the box's own shim log), so there was no sandbox to test |
| k8s | the engine's forward proxy when the guest can reach the daemon | not attempted; this matrix is about the three drivers above |

The microsandbox driver's own mechanism, with the proxy unavailable
(`HTTP_LISTEN` empty, so the sandbox carries no proxy environment), on a real KVM
run:

| Row | Observed |
| --- | --- |
| no declaration | `https://example.com/` 200, the host fixture 200 |
| `default: deny-all`, no allowance | the guest cannot resolve the name (`curl` exit 6); no connection is attempted |
| `default: deny-all` + allow `example.com:80 http` | 200 |
| `default: deny-all` + allow an address literal | the fixture 200; another address, another port, and no allowance all fail immediately (exit 7) |
| `default: deny-all` + allow `example.com:443 https` | TCP connects and the TLS handshake fails (`curl` exit 35, twice) |
| any refused row | the daemon records nothing: these refusals are not decisions the engine makes |

The strict half of the behavior is pinned as well: with
`EGRESS_UNENFORCEABLE_POLICY=refuse`, a declaration the driver cannot deliver
refuses the start instead of running the agent with unrestricted egress
(`TestE2EDockerDeclaredPolicyFailsClosedWithoutAReachableProxy`, which asserts
that the refusal names the driver and the reason and that the command never ran).

Two findings from running it:

- The microsandbox driver's `mkfs.ext4` capability check matched an English
  usage line, so a host with a translated tool reported a missing capability and
  the start failed. The probe now asks for the C locale, and
  `TestValidateMicrosandboxDiskToolsReadsTheCapabilityInTheCLocale` pins it.
- An allowance over TLS is not usable through the SDK mechanism alone (the row
  above). The proxy is what carries an HTTPS allowance today, which is the
  mechanism the engine installs whenever the sandbox can reach it.

## Still open

- **The bypass list exempts a host, not an endpoint.** `NO_PROXY` names the
  daemon's own address so a sandbox can reach the LLM facade, the telemetry
  endpoint, and the Jupyter proxy without the proxy deciding those connections.
  The list carries hosts and not ports, so every other port on that address is
  exempt as well: no declaration can allow or refuse it, and the matrix had to
  put its address fixture on a second host address for those rows to reach the
  proxy at all. Narrowing the exemption to the engine's endpoint addresses and
  ports is the fix; it needs a client survey first, because `NO_PROXY` entries
  that carry a port are not handled identically by every HTTP client.
- **Per-sandbox policy record — delivered, with one open driver gap.** A sandbox's
  runtime state records the generation of the effective policy it was created
  under and the mechanism that applied it (`open`, `driver-applied`,
  `declared-unenforced`, `mediated`), and a start that reuses a runtime reports
  the difference instead of assuming the declaration in force reaches it
  (`egress.RuntimePolicyDriftBetween`). Live on the Docker driver: a retained
  runtime created before a declaration reports *not decided by the policy now in
  force* and keeps its record, while an edit under the forward proxy reports as
  already decided, because the proxy reads the declaration per request. The open
  gap: the start path only *ensures* a runtime, so `SandboxVMInfo.RuntimeCreated`
  is what distinguishes a runtime built now from one started again. The Docker
  driver reports it; BoxLite, Microsandbox, and k8s do not yet, and are treated as
  reuse, which reports a gap rather than claiming enforcement. The report is also
  only in the daemon log today; it belongs on a per-sandbox read path.
- **The engine auto-allow is reported per mechanism, not per agent at
  admission.** `ReportNetworkPolicy`'s engine-endpoint list needs a sandbox's own
  facade address, which does not exist until the sandbox does, so the admission
  report names the exemption through the mechanism's notes and the driver's
  `egress_policy` capability instead of inventing an address. Listing the exact
  endpoints of a *running* sandbox is the remaining step.
