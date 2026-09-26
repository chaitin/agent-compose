# Scheduler JavaScript API

Scheduler scripts run in the daemon's QuickJS environment. The host injects a
global `scheduler` object; scripts do not use `import`, `require`, or Node.js
modules.

## Register stable triggers

```js
scheduler.interval("heartbeat", () => scheduler.agent("Check the workspace."), 60000);
scheduler.timeout("startup", () => scheduler.log("started"), 1000);
scheduler.cron("daily", "0 9 * * *", () => scheduler.agent("Prepare a report."), {
  timezone: "UTC",
});
scheduler.on("agent-compose.session.created", "session-created", (event) => {
  scheduler.log("new session", event);
});
```

Trigger IDs are persisted identities, not timer handles. Keep them explicit and
stable; changing one creates a different trigger. A script scheduler and
declarative `scheduler.triggers` are mutually exclusive.

## Capabilities and results

Scripts can call `scheduler.agent`, `scheduler.llm`, `scheduler.exec`,
`scheduler.shell`, `scheduler.log`, and `scheduler.event.publish`. The
`scheduler.agent` call can select `sandboxPolicy` (`new`, `sticky`, or `reuse`),
an agent, timeout, driver, guest image, workspace, environment, and an output
schema. Results include success information, output, sandbox identity, and
stop reason.

Use `scheduler.state` for JSON-serializable durable state:

```js
const count = scheduler.state.get("count") ?? 0;
scheduler.state.set("count", count + 1);
```

## Validation and execution

Validation registers triggers and checks their shape; it does not execute agent,
LLM, or shell work. Execution happens when a trigger fires or when a scheduler
run is invoked. Keep callbacks bounded with timeouts and return
JSON-serializable values so `result_json` can be persisted.

The scheduler-wide `concurrency_policy` is `skip` or `parallel`. With `skip`,
an overlapping run is recorded as skipped rather than queued. The policy
applies to the whole scheduler, not only one trigger. `scheduler.agent.async`
always uses a new sandbox for parallel work.

## Debugging

Use explicit trigger IDs, `scheduler.log`, and the CLI commands below:

```bash
agent-compose scheduler inspect <scheduler>
agent-compose scheduler invoke <scheduler> --payload '{"source":"manual"}'
agent-compose scheduler runs <scheduler>
agent-compose scheduler logs <scheduler>
```

`clearInterval` and `clearTimeout` only affect registrations made during the
current script evaluation; use the UI or API to persistently disable a trigger.
