# Scheduler script URL

Languages: English | [中文](README.zh-CN.md)

The QJS scheduler is stored in `scheduler.js` and referenced with
`scheduler.script.url`.

```bash
agent-compose config
agent-compose up
agent-compose scheduler ls reviewer
agent-compose scheduler inspect reviewer source-loaded
# Wait until scheduler inspect reports that source-loaded has fired.
agent-compose down
```

`config` and `up` resolve the relative path from the compose directory and send
an inline content snapshot to the daemon. Editing `scheduler.js` takes effect on
the next `up`; this is not a runtime import or background refresh.

The two-second timeout trigger runs a local shell command, so provider
authentication is not required. It is driven by the scheduler runtime rather
than `scheduler trigger`, whose project-run path is intended for agent prompts.
