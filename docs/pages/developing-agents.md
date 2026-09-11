# Developing agents with agent-compose

This guide connects the compose file, the daemon, the guest runtime, and the
execution records into one runnable workflow. Use it when you are building an
agent rather than only configuring one.

## Three execution environments

agent-compose has three deliberately separate JavaScript surfaces:

| Surface | Runs in | Main purpose |
| --- | --- | --- |
| Guest Runtime SDK | A sandbox guest | Run workspace code, commands, agents, and workflows |
| Scheduler API | The daemon's QuickJS runtime | Register triggers and start agents, LLM calls, commands, and events |
| Daemon API | An external client over Connect RPC | Manage projects, runs, sandboxes, and events |

Code in one surface is not automatically importable from another. Keep
workspace automation in the Guest Runtime SDK, scheduling policy in a
`scheduler.script`, and lifecycle control in the daemon client.

## A minimal project

Create `agent-compose.yml`:

```yaml
name: review-demo
agents:
  reviewer:
    provider: codex
    image: chaitin/agent-compose-guest:latest
    workspace:
      path: .
    system_prompt: Review the workspace and summarize the result.
```

Validate and apply it from the directory containing the file:

```bash
agent-compose config
agent-compose up -f agent-compose.yml
agent-compose project ls
agent-compose agent ls review-demo
agent-compose run review-demo/reviewer "Check the README for missing steps."
agent-compose logs review-demo
```

`config` parses, validates, normalizes, and redacts local configuration.
`up` stores a revision in the daemon. A run executes in an isolated sandbox;
the source workspace is not modified directly.

## Add a scheduler

For simple schedules, use declarative triggers. For shared workflow logic or
multiple capabilities, use a script:

```yaml
agents:
  reviewer:
    provider: codex
    scheduler:
      script: |
        scheduler.cron("daily-review", "0 9 * * *", async () => {
          return scheduler.agent("Review the latest workspace changes.", {
            sandboxPolicy: "new",
            timeout: "10m",
          });
        }, { timezone: "UTC" });

        function main(payload) {
          return { accepted: true, payload: payload ?? null };
        }
```

Run `agent-compose scheduler inspect review-demo/reviewer` to inspect the
stored scheduler and `agent-compose scheduler runs review-demo/reviewer` to
inspect outcomes. Scheduler scripts are evaluated by the daemon; they do not
support `import` or `require`. See the [Scheduler API guide](scheduler-api.html)
for trigger IDs, events, concurrency, cancellation, and error behavior.

## Observe and debug a run

Start with the run or scheduler-run record, then inspect its events and logs:

```bash
agent-compose inspect review-demo/reviewer
agent-compose logs review-demo
agent-compose scheduler runs review-demo/reviewer
```

The useful distinction is:

- `failed`: the operation ran and reported a failure;
- `stopped`: the operation was explicitly stopped;
- `skipped`: a scheduler concurrency policy prevented a new run;
- unsupported capability: the concrete handler rejected an unavailable driver
  or provider.

Do not treat a healthy daemon or a successful `config` as proof that a provider
or runtime image can execute. Run a real sandbox task before production use.

## Deployment checklist

Before sharing a project:

1. Pin moving Git sources and guest images to a release or commit where
   reproducibility matters.
2. Provide only the provider and guest-image capabilities the project uses.
3. Keep secrets in secret-marked environment entries; never put tokens in
   prompts, source control, or logs.
4. Set an explicit scheduler timeout and concurrency policy for recurring work.
5. Verify a real run, its persisted events, and its failure path with the
   selected runtime driver.
