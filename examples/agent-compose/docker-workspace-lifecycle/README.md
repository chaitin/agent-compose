# Docker workspace lifecycle

Languages: English | [中文](README.zh-CN.md)

This example copies `workspace/` into each new sandbox and demonstrates the
current sandbox lifecycle without model credentials.

## Prerequisites

- Docker and the `agent-compose` daemon are running.
- The published guest image is available to Docker.

## Configuration

The `source` workspace uses the local provider and `path: ./workspace`. The
`worker` agent refers to it by name. A new sandbox receives a copy; the host
source directory is not mounted as a writable working tree.

## Run the tutorial

```bash
agent-compose up
agent-compose run worker --command "printf 'sandbox-only\\n' > generated.txt" --keep-running
agent-compose ps
agent-compose exec <sandbox-id> -- cat generated.txt
agent-compose stop <sandbox-id>
agent-compose resume <sandbox-id>
agent-compose exec <sandbox-id> -- cat generated.txt
agent-compose stop <sandbox-id>
agent-compose rm <sandbox-id>
agent-compose down
```

`generated.txt` survives stop/resume but is not written into the committed
`workspace/` source. A second new sandbox receives a fresh source copy.

## Verification and cleanup

Use the sandbox id returned by `run`. Confirm the file before and after
stop/resume, then check that `workspace/generated.txt` does not exist on the
host. `rm` deletes the stopped sandbox; `down` cleans remaining project state.

## Real verification output

Captured from the real-daemon Docker E2E on 2026-07-15:

```console
status=succeeded
run=7f1f9aaf0cd1d1c125c51bac9f915cdcf57aa9d531353beb9e77faa4ed4109d7
sandbox=8eac6735af343c91804590c5329d57e274a1e395c6a3ecb41f4c0c62c1ff4629
$ agent-compose exec <sandbox-id> -- cat generated.txt
sandbox-only
```

Run and sandbox IDs are generated and will differ.
