# Docker volume persistence

Languages: English | [中文](README.zh-CN.md)

This project mounts a managed named volume at `/cache` and a read-only bind
fixture at `/fixtures`.

## Prerequisites and configuration

Docker and the daemon must be running. The top-level `cache` volume is managed
by the project and mounted read-write. `./fixtures:/fixtures:ro` resolves from
the compose directory and is mounted read-only.

## Run the tutorial

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

## What to verify

Use the sandbox id returned by the kept run. The first command must read the
fixture and write `/cache/value`; after stop/resume, `cat` must return
`persistent`. The `touch` check must fail inside the read-only mount. Stop and
remove the sandbox before `down` for an explicit lifecycle cleanup.

## Real verification output

Captured after a real Docker sandbox stop/resume on 2026-07-15:

```console
$ agent-compose exec <sandbox-id> -- cat /cache/value
persistent
$ agent-compose exec <sandbox-id> -- sh -c 'if touch /fixtures/unexpected 2>/dev/null; then exit 1; fi'
# exit status 0: writing to the read-only mount was rejected
```
