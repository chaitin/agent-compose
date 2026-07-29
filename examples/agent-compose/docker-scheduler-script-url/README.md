# Scheduler script file-source example

Languages: English | [中文](README.zh-CN.md)

This example keeps QJS in `scheduler.js` and references it from
`agent-compose.yml` with `scheduler.script.provider: file` and `path`.

To load the script over HTTP instead, replace the `script` mapping with:

```yaml
script:
  provider: http
  url: https://example.com/scheduler.js
```

`agent-compose config` and `agent-compose up` fetch the URL and send a content
snapshot to the daemon. The daemon does not refresh the URL at runtime.

```bash
agent-compose config
agent-compose up
agent-compose ls
agent-compose scheduler ls
agent-compose scheduler invoke reviewer --payload '{"source":"example"}'
agent-compose down
```

`config` prints the fetched script inline. `up` fetches it once more, hashes the
content snapshot, and sends only script text to the daemon. `ls` shows the
managed agent and scheduler, while `scheduler ls` shows the registered
`daily-review` trigger. `scheduler invoke` calls the script's `main(payload)`
entrypoint without waiting for the cron trigger. Editing `scheduler.js` takes
effect on the next `up`. The relative path is resolved from the directory
containing `agent-compose.yml`.

The control-plane commands do not require provider authentication. A scheduled
run does require a working guest runtime and provider credentials.
