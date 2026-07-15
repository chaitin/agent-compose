# Scheduler script URL example

Languages: English | [中文](README.zh-CN.md)

This example keeps QJS in `scheduler.js` and references it from
`agent-compose.yml` with `scheduler.script.url`.

The script keeps the original daily `scheduler.cron(...)` agent callback and
also defines a two-second `scheduler.timeout(...)` shell callback. The timeout
provides a quick way to verify that the external file was loaded; it does not
replace the cron tutorial.

## Run step by step

```bash
agent-compose config
agent-compose up
agent-compose ps
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer daily-review
agent-compose scheduler inspect reviewer source-loaded
agent-compose down
```

`config` prints the fetched script inline. `up` fetches it once more, hashes the
content snapshot, and sends only script text to the daemon. Editing
`scheduler.js` takes effect on the next `up`. The relative path is resolved from
the directory containing `agent-compose.yml`.

The control-plane commands do not require provider authentication. A scheduled
agent callback requires a working guest runtime and daemon provider
configuration. The `source-loaded` shell callback does not call a model.

Expected checks:

1. `config` contains the resolved QJS content.
2. `scheduler ls reviewer` lists `daily-review` and `source-loaded`.
3. After about two seconds, `source-loaded` has fired and its event contains
   `scheduler script URL ok`.
4. `daily-review` remains scheduled for `0 9 * * *` and calls the agent when its
   calendar time arrives.
5. `down` disables the scheduler and cleans project sandboxes.

## Example successful output

When the `source-loaded` callback succeeds, its event looks like:

```console
type=loader.command.completed
message="scheduler script URL ok"
payload={"exitCode":0,"mode":"shell","stderrTruncated":false,"stdoutTruncated":false,"success":true}
```

The full event also contains generated cell and sandbox IDs, omitted here for
readability. The message and success fields confirm the script ran.
