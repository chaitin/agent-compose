# Scheduler script URL example

Languages: English | [中文](README.zh-CN.md)

This example keeps QJS in `scheduler.js` and references it from
`agent-compose.yml` with `scheduler.script.url`.

It demonstrates two independent concerns:

- `daily-review` keeps the tutorial's calendar-based
  `scheduler.cron(...) + scheduler.agent(...)` flow.
- `source-loaded` is a two-second `scheduler.timeout(...)` shell callback used
  to prove, with a real daemon, that the external script was loaded and run.

## Prerequisites

- Docker daemon and the `agent-compose` daemon are running.
- Docker can pull the published guest image, or it already exists locally.
- The cron agent callback requires a configured LLM provider when it fires.
  The timeout shell callback does not use a model.

## Compose and script

The compose file enables the scheduler and points to a path relative to the
compose directory:

```yaml
scheduler:
  enabled: true
  sandbox_policy: new
  script:
    url: ./scheduler.js
```

The script preserves the real cron agent example and adds a fast verification
trigger:

```js
scheduler.cron("daily-review", "0 9 * * *", function dailyReview() {
  return scheduler.agent("Review the current project state.");
});

scheduler.timeout("source-loaded", function sourceLoaded() {
  return scheduler.shell("printf 'scheduler script URL ok\\n'");
}, 2000);
```

## Run the example

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer daily-review
agent-compose scheduler inspect reviewer source-loaded
agent-compose down
```

`config` and `up` resolve the relative URL, read the script, and send an inline
content snapshot to the daemon. Editing `scheduler.js` takes effect on the next
`up`; it is not a runtime import or background refresh.

## What to verify

- `config` includes the resolved script content.
- `scheduler ls reviewer` lists both `daily-review` and `source-loaded`.
- after about two seconds, inspecting `source-loaded` shows that it fired and
  its event contains `scheduler script URL ok`.
- `daily-review` remains scheduled for `0 9 * * *` and invokes the agent when
  its calendar time arrives.
- `down` disables the scheduler and removes project sandboxes.

Generated loader IDs and timestamps vary by environment. The real-daemon E2E
asserts the event message instead of hard-coding those values.
