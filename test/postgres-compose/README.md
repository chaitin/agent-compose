# PostgreSQL functional test stack

This Compose stack runs the daemon built from this worktree against PostgreSQL,
plus a second SQLite daemon used to seed data for the offline copy test.

## Build the images

From the repository root:

```bash
PROXY=http://proxy.in.chaitin.net:8123
REGISTRY_MIRROR=registry-mirrors.dev.in.chaitin.net \
HTTP_PROXY=$PROXY HTTPS_PROXY=$PROXY http_proxy=$PROXY https_proxy=$PROXY \
IMAGE_TAG=agent-compose:postgres-test \
task image:agent-compose

REGISTRY_MIRROR=registry-mirrors.dev.in.chaitin.net \
HTTP_PROXY=$PROXY HTTPS_PROXY=$PROXY http_proxy=$PROXY https_proxy=$PROXY \
IMAGE_TAG=agent-compose-guest:local \
task image:agent-compose-guest
```

`agent-compose-guest:local` is also used by the daemon's Docker sandboxes, so it
must exist even when only the daemon image is rebuilt.

## Configure and run

```bash
cd test/postgres-compose
cp .env.example .env
# set POSTGRES_PASSWORD, AGENT_COMPOSE_IMAGE, GUEST_IMAGE; optionally LLM_*
./run-functional-tests.sh
```

The script starts PostgreSQL on `127.0.0.1:55433` and the daemon on
`127.0.0.1:7411`. It keeps the stack and its volumes by default; set
`KEEP=0` to remove everything after the run. Set `RUN_AGENT=1` to attempt a real
LLM agent run; the run row and events are verified whether the external model
responds or not.

## What is covered

- PostgreSQL baseline migration, schema types, and `db status`.
- Project/agent/scheduler/volume CRUD through the public CLI/API.
- Scheduler script execution in a Docker sandbox and event publication.
- Command run, named volume persistence, sandbox stop/resume/exec/remove.
- Daemon restart: state survives and migrations are not reapplied.
- PostgreSQL restart: the daemon reacquires the single-instance advisory lock;
  a second daemon is still refused.
- SQLite default backend, then offline `db copy` into `copy_target`, including
  row-count verification and refusal to copy into a non-empty target.

The PostgreSQL instance lock is scoped to a database. When running the Go
integration tests against an already-running test stack, use a separate
database (for example `agent_compose_test`) rather than `agent_compose`,
otherwise the daemon holds the lock.
