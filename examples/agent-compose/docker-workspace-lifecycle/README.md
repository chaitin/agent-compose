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
