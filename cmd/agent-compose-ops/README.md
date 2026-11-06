# agent-compose-ops

`agent-compose-ops` is the read-only operations helper for an `agent-compose`
deployment. It answers the question an operator asks before restarting the
daemon: **"what is running right now, and when is a good time to upgrade?"**

It reads the daemon's persisted SQLite database directly:

- opens `${DATA_ROOT}/data.db` with `mode=ro` and `PRAGMA query_only`, and
- never runs migrations, never writes, and never calls the daemon API.

The daemon keeps running and writing while the helper reads the database. An
upgrade itself still restarts the daemon; this helper only helps you choose the
moment and understand what will be interrupted.

## Build and run

```bash
# from the repository root
task build:agent-compose-ops
./build/agent-compose-ops --help

# or run without installing
go run ./cmd/agent-compose-ops upgrade status --data-root ./data
```

The daemon image also contains the binary:

```bash
docker exec agent-compose agent-compose-ops upgrade status
```

Inside the image `DATA_ROOT=/data`, so no extra flags are needed.

## Commands

```bash
# Live snapshot: running scheduler runs, project runs, sandboxes, failures, anomalies
agent-compose-ops upgrade status --data-root /srv/agent-compose/data

# History: runs, failures, duration percentiles, busiest hours per scheduler
agent-compose-ops upgrade stats --days 14 [--triggers]

# Upcoming scheduled trigger fires
agent-compose-ops upgrade upcoming --horizon 48h --limit 50

# Recommended restart windows
agent-compose-ops upgrade windows \
  --days 14 --horizon 48h --window 10m --downtime 90s \
  [--top 5] [--assume-pause]

# Evaluate one proposed time
agent-compose-ops upgrade forecast --at "2026-11-03 02:00" --tz Asia/Shanghai
```

All commands accept `--json` for scripting and `--tz` for report timestamps.
`--db` points directly at a `data.db` and overrides `--data-root`.

Trigger forecasts only consider triggers that are enabled on an **enabled**
scheduler; a disabled scheduler keeps its trigger rows as historical
configuration and is not treated as ticking. `upgrade upcoming` reports
`total_fires` and `truncated`: when `truncated` is true, raise `--limit` or
narrow `--horizon` before treating the list as a complete calendar.

### How to read `windows`

Candidates are ranked by an estimated interruption score (lower is better):

| Column | Meaning |
| --- | --- |
| `SCORE` | `expected historical in-flight + expected current in-flight + optional fire penalty` |
| `IN-FLIGHT` | average number of scheduler runs in flight at this time of week over the history window |
| `CURRENT` | contribution of scheduler runs that are running right now |
| `FIRES` | scheduled trigger fires inside the window |
| `RUNS/WK` | historical run starts per week for this time of week |

By default the recommendation penalizes scheduled fires inside the window,
because the daemon does not defer them yet. Once the upgrade gate is deployed,
pass `--assume-pause` so fires inside the window are treated as deferred until
after the restart and no longer penalize a candidate.

The score is a heuristic. A long scheduler run that is already in progress can
still be interrupted; `status` shows those runs explicitly, and
`forecast --at` evaluates a specific planned time.

## Exit codes

| Code | Meaning |
| --- | --- |
| `0` | Report produced |
| `1` | Invalid arguments, unreadable database, or a non-agent-compose database |

## Limitations

- History is limited by how far back `scheduler_run` rows were retained. The
  daemon only deletes them through an explicit `scheduler prune`.
- Scheduled triggers are predictable from their cron/interval/timeout
  definition. Topic/event triggers depend on external events and are not
  forecast.
- The helper is not a daemon health check; use `/api/version` for that.
- The default window estimate is a heuristic; calibrate `--downtime` on the
  target host and pair the recommendation with the upgrade runbook.

## Development

```bash
task build:agent-compose-ops
go test ./cmd/agent-compose-ops/... ./pkg/upgradeadvisor/... ./pkg/storage/sqlite/...
```
