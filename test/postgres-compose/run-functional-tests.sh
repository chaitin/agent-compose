#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

if [[ ! -f .env ]]; then
  echo "missing .env; copy .env.example to .env and fill it" >&2
  exit 2
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

COMPOSE=(docker compose)
KEEP="${KEEP:-1}"
RUN_AGENT="${RUN_AGENT:-0}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

pass() {
  echo "PASS: $*"
}

ac() {
  "${COMPOSE[@]}" exec -T agent-compose /app/agent-compose --host http://127.0.0.1:7410 "$@"
}

ac_sqlite() {
  "${COMPOSE[@]}" exec -T agent-compose-sqlite /app/agent-compose --host http://127.0.0.1:7410 "$@"
}

pg() {
  "${COMPOSE[@]}" exec -T postgres psql -U agent_compose -d agent_compose -Atc "$1"
}

assert_contains() {
  local haystack=$1 needle=$2 message=$3
  grep -Fq -- "$needle" <<<"$haystack" || fail "$message (missing: $needle)"
}

assert_eq() {
  local actual=$1 expected=$2 message=$3
  [[ "$actual" == "$expected" ]] || fail "$message: got '$actual', want '$expected'"
}

assert_ge() {
  local actual=$1 minimum=$2 message=$3
  [[ "$actual" =~ ^[0-9]+$ && "$actual" -ge "$minimum" ]] || fail "$message: got '$actual', want >= $minimum"
}

json_value() {
  python3 -c 'import json,sys
data=json.load(sys.stdin)
for part in sys.argv[1].split("."):
    if part.endswith("]"):
        name, index = part[:-1].split("[")
        data = data[name][int(index)]
    else:
        data = data[part]
print(data)' "$1"
}

wait_daemon() {
  local service=$1
  for _ in $(seq 1 90); do
    if "${COMPOSE[@]}" exec -T "$service" /app/agent-compose --host http://127.0.0.1:7410 status >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  "${COMPOSE[@]}" logs "$service" | tail -40
  fail "$service did not become ready"
}

wait_postgres() {
  for _ in $(seq 1 60); do
    if "${COMPOSE[@]}" exec -T postgres psql -U agent_compose -d agent_compose -Atc 'select 1' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  fail "PostgreSQL did not become ready"
}

if [[ "$KEEP" != "1" ]]; then
  "${COMPOSE[@]}" --profile sqlite --profile copy down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf data
fi
mkdir -p data

echo "== start PostgreSQL"
"${COMPOSE[@]}" up -d postgres >/dev/null
wait_postgres
"${COMPOSE[@]}" exec -T postgres psql -U agent_compose -d agent_compose -c 'CREATE SCHEMA IF NOT EXISTS copy_target;' >/dev/null

echo "== start daemon"
"${COMPOSE[@]}" up -d agent-compose >/dev/null
wait_daemon agent-compose
pass "daemon started on PostgreSQL"

echo "== migration, schema, and db status"
status=$(ac db status)
assert_contains "$status" "driver: postgres" "db status driver"
assert_contains "$status" "applied=1 latest=1 pending=0" "db status migration state"
assert_eq "$(pg 'select max(version) from schema_migrations')" "1" "migration version"
assert_eq "$(pg "select count(*) from information_schema.tables where table_schema='public' and table_type='BASE TABLE'")" "31" "application table count"
assert_eq "$(pg "select data_type from information_schema.columns where table_name='event' and column_name='sequence'")" "bigint" "event sequence type"
assert_eq "$(pg "select data_type from information_schema.columns where table_name='sandboxes' and column_name='created_at'")" "bigint" "sandbox timestamp type"
pass "PostgreSQL baseline and migration state"

echo "== project, agent, scheduler, volume CRUD"
ac project up -f /projects/pg-functional/agent-compose.yml >/dev/null
projects=$(ac project ls --json)
assert_contains "$projects" '"name": "pg-functional"' "project list"
assert_contains "$(ac agent ls -p pg-functional --json)" '"name": "worker"' "agent list"
assert_contains "$(ac scheduler ls -p pg-functional --json)" '"name": "pg-functional"' "scheduler list"
assert_eq "$(pg 'select count(*) from project')" "1" "project row"
assert_eq "$(pg 'select count(*) from project_agent')" "1" "agent row"
assert_eq "$(pg 'select count(*) from project_scheduler')" "1" "scheduler row"
assert_eq "$(pg 'select count(*) from volumes')" "1" "volume row"
assert_eq "$(pg 'select count(*) from project_volumes')" "1" "project volume link"
assert_eq "$(pg 'select count(*) from project_revision')" "1" "project revision"
pass "project/agent/scheduler/volume persisted in PostgreSQL"

echo "== scheduler script: sandbox shell + event publish"
scheduler_result=$(ac scheduler invoke worker -p pg-functional --json)
assert_contains "$scheduler_result" '"status": "succeeded"' "scheduler invoke status"
assert_contains "$scheduler_result" "pg-scheduler-ok" "scheduler sandbox shell output"
assert_eq "$(pg "select count(*) from event where topic='runtime.pg.functional.event'")" "1" "scheduler event row"
assert_eq "$(pg "select sequence from event where topic='runtime.pg.functional.event'")" "1" "scheduler event sequence"
pass "scheduler executed in a Docker sandbox and published an event"

echo "== command run, volume, sandbox lifecycle"
run_json=$(ac run worker -p pg-functional --command "echo pg-sandbox-ok && echo run-probe > /mnt/data/run-probe && cat /mnt/data/run-probe" --keep-running --json)
assert_contains "$run_json" '"status": "succeeded"' "command run status"
assert_contains "$run_json" "pg-sandbox-ok" "command output"
sandbox_id=$(json_value <<<"$run_json" sandbox_id)
run_id=$(json_value <<<"$run_json" id)
[[ -n "$sandbox_id" && -n "$run_id" ]] || fail "run did not report sandbox and run ids"
assert_eq "$(pg "select status from project_run where run_id='$run_id'")" "succeeded" "persisted run status"
assert_ge "$(pg "select count(*) from project_run_event where run_id='$run_id'")" "1" "persisted run events"
ps_json=$(ac ps -a -p pg-functional --json)
assert_contains "$ps_json" "$sandbox_id" "sandbox list"
exec_json=$(ac exec "$sandbox_id" -p pg-functional --command "cat /mnt/data/run-probe && hostname" --json)
assert_contains "$exec_json" "run-probe" "volume content in running sandbox"
assert_contains "$(ac stop "$sandbox_id" -p pg-functional)" "stopped sandbox" "sandbox stop"
assert_contains "$(ac resume "$sandbox_id" -p pg-functional)" "resumed sandbox" "sandbox resume"
assert_contains "$(ac exec "$sandbox_id" -p pg-functional --command "echo resume-ok" --json)" "resume-ok" "exec after resume"
ac stop "$sandbox_id" -p pg-functional >/dev/null
assert_contains "$(ac rm "$sandbox_id" -p pg-functional)" "removed sandbox" "sandbox remove"
pass "command run, named volume, stop/resume/remove"

echo "== daemon restart keeps PostgreSQL state"
"${COMPOSE[@]}" restart agent-compose >/dev/null
wait_daemon agent-compose
assert_contains "$(ac project ls --json)" '"name": "pg-functional"' "project after daemon restart"
assert_eq "$(pg 'select max(version) from schema_migrations')" "1" "migration after restart"
assert_eq "$(pg 'select count(*) from project')" "1" "project rows after restart"
pass "daemon restart preserved state and did not reapply migrations"

echo "== PostgreSQL restart: lock is reacquired"
"${COMPOSE[@]}" stop postgres >/dev/null
sleep 8
"${COMPOSE[@]}" start postgres >/dev/null
wait_postgres
for _ in $(seq 1 60); do
  if ac project ls >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
assert_contains "$(ac project ls --json)" '"name": "pg-functional"' "project after PostgreSQL restart"
set +e
second_out=$(timeout 25s docker run --rm --network acpg_default --name acpg-second \
  -e DB_DRIVER=postgres \
  -e DB_URL="postgres://agent_compose:${POSTGRES_PASSWORD}@postgres:5432/agent_compose?sslmode=disable" \
  -e DATA_ROOT=/tmp/ac-second -e HTTP_LISTEN=127.0.0.1:7410 \
  -e AGENT_COMPOSE_SOCKET=/tmp/ac-second.sock \
  "${AGENT_COMPOSE_IMAGE}" daemon 2>&1)
second_rc=$?
set -e
assert_eq "$second_rc" "1" "second daemon exit code"
assert_contains "$second_out" "another agent-compose instance" "second daemon refused after PostgreSQL restart"
pass "instance lock reacquired after PostgreSQL restart"

echo "== SQLite default backend + offline copy"
"${COMPOSE[@]}" --profile sqlite up -d agent-compose-sqlite >/dev/null
wait_daemon agent-compose-sqlite
ac_sqlite project up -f /projects/pg-functional/agent-compose.yml >/dev/null
assert_contains "$(ac_sqlite project ls --json)" '"name": "pg-functional"' "SQLite project list"
sqlite_run=$(ac_sqlite run worker -p pg-functional --command "echo sqlite-run-ok" --rm --json)
assert_contains "$sqlite_run" "sqlite-run-ok" "SQLite command run"
"${COMPOSE[@]}" stop agent-compose-sqlite >/dev/null
copy_out=$("${COMPOSE[@]}" --profile copy run --rm copy 2>&1)
assert_contains "$copy_out" "copied 30 tables" "offline copy table count"
assert_eq "$(pg 'select count(*) from copy_target.project')" "1" "copied project"
assert_eq "$(pg 'select count(*) from copy_target.project_agent')" "1" "copied agent"
assert_eq "$(pg 'select count(*) from copy_target.project_scheduler')" "1" "copied scheduler"
assert_eq "$(pg 'select count(*) from copy_target.volumes')" "1" "copied volume"
assert_eq "$(pg 'select count(*) from copy_target.project_run')" "1" "copied run"
assert_eq "$(pg 'select max(version) from copy_target.schema_migrations')" "1" "copied target migration"
set +e
second_copy=$("${COMPOSE[@]}" --profile copy run --rm copy 2>&1)
second_copy_rc=$?
set -e
[[ "$second_copy_rc" -ne 0 ]] || fail "second copy unexpectedly succeeded"
assert_contains "$second_copy" "not empty" "copy refuses non-empty target"
pass "SQLite default backend and offline SQLite->PostgreSQL copy"

if [[ "$RUN_AGENT" == "1" && -n "${LLM_API_ENDPOINT:-}" ]]; then
  echo "== real agent run (optional)"
  agent_stdout=$(mktemp)
  agent_stderr=$(mktemp)
  set +e
  ac run worker -p pg-functional --prompt "Reply with exactly: pg-agent-ok" --rm --json >"$agent_stdout" 2>"$agent_stderr"
  agent_rc=$?
  set -e
  agent_run_id=$(json_value <"$agent_stdout" id 2>/dev/null || true)
  if [[ -z "$agent_run_id" ]]; then
    agent_run_id=$(pg "select run_id from project_run where project_name='pg-functional' order by created_at desc limit 1")
  fi
  [[ -n "$agent_run_id" ]] || fail "agent run was not persisted"
  agent_status=$(pg "select status from project_run where run_id='$agent_run_id'")
  [[ "$agent_status" == "succeeded" || "$agent_status" == "failed" ]] || fail "agent run status '$agent_status' was not persisted"
  assert_ge "$(pg "select count(*) from project_run_event where run_id='$agent_run_id'")" "1" "agent run events"
  echo "agent run exit=$agent_rc status=$agent_status"
  echo "LLM success is environment-dependent; the run row/events are persisted either way."
  if [[ "$agent_status" == "failed" ]]; then
    echo "agent failure tail: $(tail -3 "$agent_stderr" | tr '\n' ' ')"
  fi
  rm -f "$agent_stdout" "$agent_stderr"
fi

echo "ALL FUNCTIONAL TESTS PASSED"
if [[ "$KEEP" != "1" ]]; then
  "${COMPOSE[@]}" --profile sqlite --profile copy down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf data
  echo "stack removed"
else
  echo "stack kept for inspection (KEEP=0 removes it)"
fi
