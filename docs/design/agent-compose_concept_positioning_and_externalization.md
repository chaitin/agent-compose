# agent-compose Concept Positioning And Externalization Boundary

Chinese version:
[../zh-CN/design/agent-compose_concept_positioning_and_externalization.md](../zh-CN/design/agent-compose_concept_positioning_and_externalization.md)

This document is the conceptual source for agent-compose's agent/service
platform direction. It defines what agent-compose should be, what it should not
own, and which concepts upper platforms should depend on.

The corresponding engineering workstream plan is
[agent-compose_engine_foundation_plan.md](agent-compose_engine_foundation_plan.md).

## Document Role

This document answers why and what:

- the product and architecture positioning of agent-compose;
- the boundaries between Project, Manifest, Agent, Service, Trigger, Runtime,
  and SDK;
- which concepts belong to the engine and which belong to upper platforms.

The engine foundation plan answers how:

- workstreams, ownership, deliverables, and acceptance criteria;
- which module-level contract documents must be updated as implementation
  lands.

The two documents are not substitutes:

- this document is the conceptual boundary;
- the foundation plan is the compressed engineering plan;
- current behavior is defined by code, proto, manifest schema, CLI help, and
  runtime contract documents.

## Core Positioning

agent-compose should be:

```text
a project-manifest control plane and runtime execution platform for agent and
service workloads.
```

The daemon is the control plane. The runtime is the execution plane. The project
manifest is the declarative desired state. Triggers describe when to run
targets. The runtime SDK is the standard toolbox for service code.

Business logic is written by users or upper platforms. agent-compose provides
the generic infrastructure to define, validate, run, observe, and govern that
logic.

## Non-Goals

agent-compose must not own product-specific concepts such as tenants, channel
bindings, organization identity, approval workflows, billing, marketplace
packaging, or product-specific permission policies.

Upper platforms may project those concepts into manifests, runtime context
metadata, capability scopes, and service schemas, but they should not become
first-class engine models.

## Core Concepts

- **Project**: a versioned desired state containing agent profiles, service
  entries, triggers, workspace, runtime constraints, and permissions.
- **Project Manifest / Compose File**: the declarative text representation of a
  project. It describes structure and file references rather than embedding
  large business implementations.
- **Agent**: an AI provider profile. It is not a complete business service.
- **Service Entry**: the main reusable execution target. It references user
  service code and declares input/output/error schemas, timeout, retry,
  permissions, agents, and examples.
- **Trigger / Scheduler / Loader**: the trigger and lightweight orchestration
  layer. Declarative triggers are the recommended path; loader JS remains an
  escape hatch.
- **Runtime**: the controlled execution environment for service entries, agent
  providers, and commands.
- **Runtime SDK**: the stable API surface for service code to call platform
  capabilities without depending on daemon-private paths or magic stdout
  payloads.

## External Integration Surface

Upper platforms should prefer:

```text
business contract
  -> project manifest projection
  -> ValidateProject / ApplyProject
  -> InvokeService / InvokeServiceStream
  -> WatchRun / GetRun / ListRuns
  -> ListArtifacts / ReadArtifact
```

Upper platforms should not depend on daemon-private database tables, session
directory internals, loader implementation details, provider-specific prompt
paths, runtime magic stdout payloads, or v1-only session fields.

## Current Engineering State

The `feature/agent-compose-engine-foundation` branch implements the main
engineering foundation for this concept:

- v2 ProjectService covers validate/apply/get/list/remove/diff/revision/
  rollback/watch.
- v2 RunService covers InvokeService, InvokeServiceStream, RunAgent, GetRun,
  ListRuns, StopRun, and WatchRun.
- Manifest supports services, triggers, runtime, workspace, permissions, and
  artifacts.
- CLI supports validate, bundle validate, bundle inspect, up, invoke, logs, and
  inspect.
- Runtime service results use structured `service-result.json`; magic stdout is
  compatibility fallback only.
- RunDetail and stream terminal events expose a standard run envelope.
- Runtime SDK exposes service/capability bridges, secrets, and mockRuntime.
- `examples/service-entry` provides a minimal service entry contract example
  covered by bundle validation.

Remaining work is mostly productization around remote bundle publishing,
signing, registry integration, upper UI workflows, full capability governance,
examples, and code-generation guidance. Those do not change the engine concept
boundary.
