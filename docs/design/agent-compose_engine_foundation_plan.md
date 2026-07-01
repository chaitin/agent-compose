# agent-compose Engine Foundation Plan

Chinese version: [../zh-CN/design/agent-compose_engine_foundation_plan.md](../zh-CN/design/agent-compose_engine_foundation_plan.md)

This document defines the target engineering plan for turning agent-compose into
a production-grade, open-source execution engine for agent and service
workloads. It is intentionally written as a final-state plan, not as a migration
plan from the current agent/session-centric implementation.

The goal is to give maintainers and parallel implementation agents a shared task
map with clear ownership boundaries, dependencies, deliverables, and acceptance
criteria.

Concept source:
[agent-compose_concept_positioning_and_externalization.md](agent-compose_concept_positioning_and_externalization.md).
That document defines the product and architecture boundary. This document
compresses that boundary into engineering workstreams. It does not replace the
concept document, and it is not the sole source of current behavior.

Current behavior is defined by the code, generated proto, manifest schema, CLI
help, and module-specific runtime/API contract documents. Items in this plan
that mention marketplace packaging, upper UI workflows, remote bundle
distribution, or product-specific permissions are intentionally out of scope for
the engine unless they are expressed through business-neutral manifest,
runtime-context, service, artifact, event, or capability contracts.

Related current documents:

- [Architecture notes](agent-compose_design.md)
- [Concept positioning and externalization boundary](agent-compose_concept_positioning_and_externalization.md)
- [Runtime JavaScript contract](agent-compose-runtime-js_contract.md)
- [Runtime environment variables](runtime_environment_variables_design.md)
- [Runtime mount manifest](runtime_mount_manifest_design.md)
- [Webhook design](webhook_design.md)
- [Runtime LLM Facade](../zh-CN/design/agent-compose-runtime-llm-facade.md)
- [OctoBus integration](octobus_integration.md)

## 1. Target Positioning

agent-compose should be a business-neutral runtime engine:

```text
project manifest -> validation/revision -> service/agent/run invocation
  -> isolated runtime -> logs/artifacts/events/metrics -> stable API/SDK
```

The engine owns generic runtime infrastructure:

- project manifest validation, apply, diff, revision, and rollback
- agent provider profiles
- service entries with input/output schemas
- trigger, webhook, event, and scheduler dispatch
- runtime context propagation
- isolated runtime execution
- run records, logs, artifacts, events, metrics, and state
- runtime SDK and host/runtime protocol
- capability gateway integration as a generic extension point

The engine must not own enterprise product concepts such as tenants, channel
bindings, organization identity resolution, approval workflows, billing,
marketplace packaging, or product-specific permission policies. Those concepts
belong in upper platforms. Upper platforms may project their business contracts
into agent-compose manifests and runtime context metadata.

## 2. Final Concept Model

### 2.1 Project Manifest

The project manifest is the declarative root object. It describes desired
runtime state, not one execution.

Target top-level sections:

- `apiVersion` / `kind`
- `metadata`: name, labels, annotations
- `variables`: project environment and secret references
- `runtime`: default driver, image, env, resources, network, cleanup
- `workspace`: default workspace source
- `agents`: AI provider profiles
- `services`: executable business-neutral entry points
- `triggers`: manual/API/cron/interval/webhook/event triggers
- `permissions`: generic capability and resource scopes
- `artifacts`: retention and storage policy

### 2.2 Agent

An agent is an AI provider profile: provider, model, system prompt, runtime
defaults, workspace defaults, and capability scope. It is not a complete
business service.

### 2.3 Service Entry

A service entry is the main reusable execution target. It has a stable name,
input schema, output schema, implementation entry file, runtime requirements,
permissions, timeout, retry policy, and examples.

Upper platforms should call service entries for product workflows. Direct agent
runs remain useful as a lower-level engine capability.

### 2.4 Trigger

A trigger describes when to invoke a target. It should target a service entry or
an agent profile with mapped input. Trigger configuration must stay separate from
business implementation code.

### 2.5 Runtime Context

Runtime context is a generic envelope for source, request ids, trace ids,
metadata, env overrides, identity metadata, and capability scope. The engine
stores, audits, injects, and forwards it without interpreting product-specific
keys.

### 2.6 Run

A run is the unified execution record for service, agent, exec, trigger, and
webhook executions. It captures input, output, error, context, status, logs,
artifacts, metrics, timeline, project revision, runtime driver, and image.

## 3. Target API Surface

The v2 API should be the final public engine API. v1 remains compatibility API
only.

### 3.1 ProjectService

Target methods:

- `ValidateProject`
- `ApplyProject`
- `GetProject`
- `ListProjects`
- `RemoveProject`
- `DiffProject`
- `ListProjectRevisions`
- `RollbackProjectRevision`
- `WatchProject`

### 3.2 RunService

Target methods:

- `InvokeService`
- `InvokeServiceStream`
- `RunAgent`
- `RunAgentStream`
- `GetRun`
- `ListRuns`
- `StopRun`
- `WatchRun`

`InvokeService` should become the primary integration path for upper platforms.
`RunAgent` remains a useful lower-level path.

### 3.3 ArtifactService

Target methods:

- `ListArtifacts`
- `GetArtifact`
- `ReadArtifact`
- `WriteArtifact`
- `DeleteArtifact`

### 3.4 EventService

Target methods:

- `PublishEvent`
- `ListEvents`
- `WatchEvents`

### 3.5 CapabilityService

The existing capability surface should be normalized as a generic gateway
contract. OctoBus remains one implementation, not the only conceptual model.

## 4. Orthogonal Workstreams

The following workstreams are intentionally split so multiple agents can work in
parallel with minimal file and concept overlap. Each workstream should land with
tests and documentation updates.

### W0. Protocol And Compatibility Governance

Owner area:

- `proto/agentcompose/v2/`
- `proto-client/`
- generated Go and TypeScript clients
- API compatibility notes in docs

Deliverables:

- Final v2 proto shape for manifest, service entry, runtime context, unified
  run, artifacts, events, and revisions.
- Field naming policy that stays business-neutral.
- Regeneration workflow and client package compatibility notes.
- API-level tests for JSON/Connect field compatibility.

Acceptance:

- New API can express service invocation, runtime context, capability scope,
  artifacts, and project revisions without v1-only fields.
- Product-specific terms such as tenant, channel, corp user, or ADP do not
  appear in v2 proto as first-class engine concepts.

Parallelism:

- This workstream should define proto messages first, then unblock W1, W3, W4,
  W5, and W6.

### W1. Manifest Model, Parser, Normalizer, And Schema

Owner area:

- `pkg/compose/`
- CLI config/up paths in `cmd/agent-compose/`
- project validation paths in `pkg/agentcompose/`
- manifest docs and examples

Deliverables:

- Target manifest sections: metadata, runtime, agents, services, triggers,
  permissions, artifacts, variables, workspace, network.
- Strict parser and normalizer with stable canonical JSON hashing.
- JSON Schema for manifests.
- File reference model for prompts, service entry files, and schema files.
- Validation errors with stable field paths.

Acceptance:

- Local CLI validation and daemon validation use the same normalizer.
- Empty/default values produce deterministic spec hashes.
- Invalid references, invalid service schema, duplicate targets, and invalid
  trigger targets return field-path errors.

Parallelism:

- Can progress after W0 defines wire shapes. It can run in parallel with W2 and
  W7.

### W2. Project Store, Revision, Diff, And Rollback

Owner area:

- `pkg/agentcompose/project_schema.go`
- `pkg/agentcompose/project_store.go`
- v2 ProjectService handlers

Deliverables:

- Revision store for normalized manifest specs.
- Apply idempotency by spec hash.
- Diff response between current/applied/incoming specs.
- Rollback by revision.
- Remove semantics for runtime resources and history.

Acceptance:

- Applying identical manifests does not create duplicate revisions.
- Rollback creates a new current revision pointing to a previous desired state.
- Diff reports created/updated/removed/unchanged resources at project, agent,
  service, trigger, permission, and runtime levels.

Parallelism:

- Depends on W1 normalized model. Store internals can be developed in parallel
  using temporary DTOs after W0 stabilizes core messages.

### W3. Runtime Context And Capability Scope

Owner area:

- v2 run/invoke proto messages
- run coordinator request structs
- session tags/env injection
- capability proxy configuration
- runtime environment docs

Deliverables:

- Generic `RuntimeContext` model with source, client request id, trace id,
  external run id, metadata, env, identity metadata, and capability scope.
- `CapabilityScope` with capset ids and generic metadata.
- Consistent injection into session metadata, runtime env, SDK context, logs,
  and run store.
- Filtering rules for reserved env/provider credentials.

Acceptance:

- Upper platforms can pass enterprise metadata without engine-specific business
  fields.
- Context is visible in run detail and available inside runtime SDK.
- Capability metadata is forwarded to the capability gateway without product
  interpretation.

Parallelism:

- Can start after W0. Should coordinate closely with W4, W5, W8, and W10.

### W4. Unified Run Store And Run Lifecycle

Owner area:

- `pkg/agentcompose/project_run*`
- RunService handlers
- stream event mapping
- run query and stop logic

Deliverables:

- Unified run target model: service, agent, exec, trigger, webhook.
- Stored input JSON, output JSON, error object, runtime context, metrics,
  artifacts, logs, timeline, driver, image, cleanup status.
- Stable stream event envelope.
- Pagination and filtering by project, target type/name, source, status,
  scheduler, trigger, request id, and time range.

Acceptance:

- `GetRun` is enough to reconstruct what was invoked, with which context, and
  what result/artifacts were produced.
- Terminal states are persisted for success, failure, cancellation, startup
  failure, validation failure, timeout, and cleanup failure.

Parallelism:

- Depends on W0. Can proceed in parallel with W5 by agreeing on service run
  target fields.

### W5. Service Entry Invocation Engine

Owner area:

- manifest service model
- new service invocation coordinator
- host/runtime request files
- service result envelope
- v2 RunService `InvokeService*`

Deliverables:

- Service entry resolution from project revision.
- JSON input validation against `inputSchema`.
- Runtime execution of entry files.
- Output validation against `outputSchema`.
- Standard result envelope with output, error, artifacts, logs, and metrics.
- Streaming invocation path.

Acceptance:

- A manifest-defined service can be invoked without directly calling an agent
  prompt.
- Invalid input fails before runtime startup when possible.
- Invalid output is reported as a structured run failure.
- Service code can call agent, LLM, exec, capability, state, artifact, log, and
  event APIs through the SDK.

Parallelism:

- Depends on W0, W1, W3, W4, and W8 contracts. Implementation can be staged with
  a minimal runtime command before full SDK completion.

### W6. Trigger, Scheduler, Webhook, And Event Targeting

Owner area:

- `pkg/agentcompose/loader_*`
- webhook/event HTTP routes
- manifest trigger model
- scheduler validation and generated loader scripts

Deliverables:

- Trigger target model for service and agent targets.
- Input mapping from static config and event/webhook payload.
- Declarative cron, interval, timeout, event, and webhook triggers.
- Loader JS retained as advanced orchestration with explicit boundaries.
- Trigger run records using the unified run model.

Acceptance:

- Declarative triggers can invoke service entries.
- Webhook/event payloads can be mapped into service input.
- Loader JS is no longer the recommended place for long-running business logic.

Parallelism:

- Depends on W1 target model and W5 invocation. Webhook queue hardening can run
  in parallel if it only touches ingress persistence.

### W7. Runtime Host/Guest Protocol

Owner area:

- `runtime/javascript/`
- `pkg/agentcompose/exec.go`
- runtime contract docs
- guest image Dockerfile

Deliverables:

- Runtime command for service entry execution.
- Request file protocol carrying service input, runtime context, schemas,
  workspace, state paths, and artifact paths.
- Response protocol carrying standard result envelope.
- Exit code and error mapping contract.
- Backward-compatible prompt and exec commands while service command becomes the
  primary service-entry path.

Acceptance:

- Host never parses ad hoc stdout for service results; result payload is
  structured and versioned.
- Runtime command can be contract-tested without starting full daemon.
- Guest image includes the matching runtime and SDK versions.

Parallelism:

- Can proceed with W8 after W5 defines the service request/response envelope.

### W8. Runtime SDK Productionization

Owner area:

- `runtime/agent-compose-runtime-sdk/`
- SDK docs and examples
- SDK tests and mock runtime

Deliverables:

- Stable SDK modules: context, log, agent, llm, exec, shell, service, state,
  artifact, event, secret, capability.
- TypeScript types for runtime context, service input/output, artifacts, and
  capability calls.
- Mock runtime for local tests.
- Dry-run helpers and schema validation helpers.
- Contract tests against runtime request/response fixtures.

Acceptance:

- Service code can be written and tested locally without a live daemon.
- SDK does not expose daemon private paths as the primary API.
- SDK examples match manifest examples.

Parallelism:

- Can progress in parallel with W7. Needs W3 context and W5 result contracts.

### W9. Artifact, Log, State, Event, And Metrics Services

Owner area:

- session state layout
- run store
- new v2 services for artifacts/events
- runtime SDK host endpoints

Deliverables:

- Artifact registry with metadata, paths, content type, size, digest, and run
  association.
- Structured log timeline and stdout/stderr stream mapping.
- Persistent state namespace per project/service/session as appropriate.
- Event timeline and publish/watch API.
- Basic run metrics: duration, exit code, output size, artifact count, runtime
  startup time.

Acceptance:

- Artifacts are discoverable through API, not only by filesystem path.
- Run timeline can explain lifecycle transitions and errors.
- SDK artifact/state/event calls work from service entries.

Parallelism:

- Can be built in slices. Artifact registry should coordinate with W4 and W5;
  event API should coordinate with W6.

### W10. Security, Secrets, And Policy Boundaries

Owner area:

- config/env filtering
- runtime env injection
- capability proxy metadata
- security docs
- tests for credential leakage

Deliverables:

- Reserved env/provider key filtering policy.
- Secret reference model in manifest and runtime context.
- Runtime context redaction rules for logs and API responses.
- Capability permission check hooks that remain implementation-neutral.
- Threat model update for service entry execution.

Acceptance:

- Provider keys are not injected into guest runtime except through intended
  facades or scoped tokens.
- Secret values are never returned by manifest/run/artifact APIs.
- Tests cover redaction, reserved env filtering, and context serialization.

Parallelism:

- Cross-cutting. Should review W1, W3, W5, W8, and W9 changes before release.

### W11. CLI And Developer Experience

Owner area:

- `cmd/agent-compose/`
- examples
- docs README files

Deliverables:

- `config` supports new manifest schema and file refs.
- `up` supports validation, apply, diff preview, and revision display.
- `run` or new `invoke` supports service invocation with JSON input.
- `logs`, `inspect`, and `ps` understand unified runs and services.
- Example projects for service entry, webhook trigger, scheduler trigger, and
  capability usage.

Acceptance:

- A developer can author, validate, apply, invoke, inspect, and debug a service
  project without upper-platform code.
- CLI output redacts secrets and surfaces field-path validation errors.

Parallelism:

- Depends on W1, W4, and W5 for full behavior. CLI help/docs can be prepared in
  parallel with stubs.

### W12. Test Strategy And Release Gates

Owner area:

- `TESTING.md`
- unit/integration/e2e tests
- CI task definitions
- release notes

Deliverables:

- Test matrix for parser, proto conversion, store migrations, invocation,
  runtime contract, SDK, driver behavior, scheduler/webhook, artifacts, and
  security redaction.
- Golden manifest fixtures.
- Runtime contract fixtures.
- Backward compatibility tests for existing agent runs where retained.
- Performance and reliability smoke tests for repeated invocation.

Acceptance:

- `task lint`, `task build`, and `task test` remain the primary gates.
- New service-entry functionality has unit, integration, and at least one
  end-to-end smoke test.
- Store migrations are tested from an empty database and representative older
  schemas.

Parallelism:

- This workstream should start early by defining fixtures and gates; each other
  workstream contributes its own tests.

## 5. Suggested Parallel Execution Groups

### Group A: Contract And Manifest

- W0 Protocol And Compatibility Governance
- W1 Manifest Model, Parser, Normalizer, And Schema
- W2 Project Store, Revision, Diff, And Rollback

This group defines the stable desired-state contract.

### Group B: Invocation Core

- W3 Runtime Context And Capability Scope
- W4 Unified Run Store And Run Lifecycle
- W5 Service Entry Invocation Engine

This group defines the runtime invocation plane.

### Group C: Runtime And SDK

- W7 Runtime Host/Guest Protocol
- W8 Runtime SDK Productionization
- W10 Security, Secrets, And Policy Boundaries

This group defines what code inside the sandbox can rely on.

### Group D: Automation And Observability

- W6 Trigger, Scheduler, Webhook, And Event Targeting
- W9 Artifact, Log, State, Event, And Metrics Services

This group makes runs observable and automatable.

### Group E: Productized Engine UX

- W11 CLI And Developer Experience
- W12 Test Strategy And Release Gates

This group turns the engine into a usable and releasable developer product.

## 6. Integration Contract For Upper Platforms

Upper platforms should eventually use only these engine surfaces:

```text
business contract -> project manifest projection
  -> ValidateProject
  -> ApplyProject
  -> InvokeService / InvokeServiceStream
  -> WatchRun / GetRun / ListRuns
  -> ListArtifacts / ReadArtifact
```

Upper platforms should not depend on:

- daemon private database tables
- session directory internals
- loader implementation details
- provider-specific prompt file paths
- runtime magic stdout payloads
- v1-only session fields for final service invocation

## 7. Documentation Update Policy

Each implementation PR should update the module document closest to the changed
contract:

- Manifest/parser changes: `agent-compose_design.md` and this plan if scope
  changes.
- Runtime host/guest protocol: `agent-compose-runtime-js_contract.md`.
- Environment injection and redaction: `runtime_environment_variables_design.md`.
- Mount or path conventions: `runtime_mount_manifest_design.md` and driver
  specific notes.
- Webhook/event changes: `webhook_design.md`.
- Capability gateway changes: `octobus_integration.md` or a new generic
  capability gateway document if OctoBus-specific wording becomes too narrow.
- Testing gates: `TESTING.md`.

Design docs should state current implemented behavior. Future plans should stay
in this document until landed.

## 8. First Implementation Milestones

The first production-grade slice should be narrow but end-to-end:

1. Add runtime context and capability scope to v2 run/invoke contracts.
2. Add manifest `services` with one JavaScript service entry type.
3. Implement `InvokeService` and `InvokeServiceStream` with input/output schema
   validation and unified run records.
4. Add runtime service command and SDK `context`, `log`, `artifact`, `agent`,
   `llm`, and `capability` basics.
5. Add CLI service invocation and one documented example.
6. Add tests for parser, API conversion, run store, runtime contract, SDK mock,
   and one Docker-driver smoke path.

This slice is the smallest version that proves the final model without pushing
business concepts into the engine.
