# Docker volume persistence

Languages: English | [中文](README.zh-CN.md)

This project mounts a managed named volume at `/cache` and a read-only bind
fixture at `/fixtures`.

```bash
agent-compose up
agent-compose run worker --command "cat /fixtures/readonly.txt && printf 'persistent\\n' > /cache/value" --keep-running
agent-compose stop <sandbox-id>
agent-compose resume <sandbox-id>
agent-compose exec <sandbox-id> -- cat /cache/value
agent-compose exec <sandbox-id> -- sh -c 'if touch /fixtures/unexpected 2>/dev/null; then exit 1; fi'
agent-compose stop <sandbox-id>
agent-compose rm <sandbox-id>
agent-compose down
```

The cache value survives sandbox stop/resume. `down` removes project-managed
volume ownership, so do not use this example as a backup mechanism.
