#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: test-agent-compose-rbac.sh --binary PATH

Start an isolated local daemon over HTTP and verify the CLI RBAC, multi-token,
revocation, audit, and environment-token rotation workflows. Docker and KVM are
not required.
EOF
}

fail() {
  printf 'test-agent-compose-rbac: %s\n' "$*" >&2
  exit 1
}

binary=''
while [[ $# -gt 0 ]]; do
  case $1 in
    --binary)
      [[ $# -ge 2 ]] || fail '--binary requires a value'
      binary=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ -n $binary ]] || fail '--binary is required'
for command_name in curl jq mktemp python3; do
  command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required"
done

case $binary in
  /*) ;;
  *) binary=$PWD/$binary ;;
esac
binary_dir=$(cd -P "$(dirname "$binary")" && pwd)
binary=$binary_dir/$(basename "$binary")
[[ -f $binary && -x $binary ]] || fail "binary is not executable: $binary"

test_root=$(mktemp -d "${TMPDIR:-/tmp}/ac-rbac.XXXXXX")
data_root=$test_root/data
work_dir=$test_root/work
runtime_root=$test_root/runtime
home_root=$test_root/home
socket_path=$test_root/agent-compose.sock
daemon_stdout=$test_root/daemon.stdout
daemon_stderr=$test_root/daemon.stderr
admin_config=$test_root/admin-config.yml
readonly_config=$test_root/readonly-config.yml
revoked_stderr=$test_root/revoked.stderr
denied_stderr=$test_root/denied.stderr
daemon_pid=''

bootstrap_token='ac_bootstrap_admin_token_0123456789abcdef'
rotated_bootstrap_token='ac_rotated_admin_token_0123456789abcdef'
active_bootstrap_token=$bootstrap_token
http_port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')
host=http://127.0.0.1:$http_port

new_token() {
  python3 -c 'import secrets; print("ac_" + secrets.token_urlsafe(32))'
}

new_request_id() {
  python3 -c 'import uuid; print(uuid.uuid4())'
}

print_diagnostic_file() {
  local label=$1 file=$2
  if [[ -s $file ]]; then
    printf '\n--- %s ---\n' "$label" >&2
    sed 's/^/  /' "$file" >&2
  fi
}

stop_daemon() {
  local pid attempt=0 forced=0 status=0
  [[ -n $daemon_pid ]] || return 0
  pid=$daemon_pid
  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    while kill -0 "$pid" 2>/dev/null && [[ $attempt -lt 100 ]]; do
      sleep 0.1
      attempt=$((attempt + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
      forced=1
      kill -KILL "$pid" 2>/dev/null || true
    fi
  fi
  if wait "$pid"; then
    status=0
  else
    status=$?
  fi
  daemon_pid=''
  [[ $forced -eq 0 && $status -eq 0 ]]
}

cleanup() {
  cleanup_status=$?
  trap - EXIT HUP INT TERM
  stop_daemon || true
  if [[ $cleanup_status -ne 0 ]]; then
    print_diagnostic_file 'daemon stdout' "$daemon_stdout"
    print_diagnostic_file 'daemon stderr' "$daemon_stderr"
    print_diagnostic_file 'read-only denied stderr' "$denied_stderr"
    print_diagnostic_file 'revoked token stderr' "$revoked_stderr"
  fi
  rm -rf "$test_root"
  exit "$cleanup_status"
}

interrupted() {
  printf 'test-agent-compose-rbac: interrupted\n' >&2
  exit 130
}

trap cleanup EXIT
trap interrupted HUP INT TERM

start_daemon() {
  local token=$1 ready=0 attempt=0
  : >"$daemon_stdout"
  : >"$daemon_stderr"
  (
    cd "$work_dir"
    unset AGENT_COMPOSE_HOST AGENT_COMPOSE_CONFIG
    export AGENT_COMPOSE_AUTH_TOKEN="$token"
    export AGENT_COMPOSE_SOCKET="$socket_path"
    export DATA_ROOT="$data_root"
    export DOCKER_HOST="unix://$test_root/docker-unavailable.sock"
    export HOME="$home_root"
    export HTTP_LISTEN="127.0.0.1:$http_port"
    export RUNTIME_DRIVER=docker
    export SANDBOX_ROOT="$data_root/sandboxes"
    export XDG_RUNTIME_DIR="$runtime_root"
    exec "$binary" daemon
  ) >"$daemon_stdout" 2>"$daemon_stderr" &
  daemon_pid=$!

  while [[ $attempt -lt 300 ]]; do
    if ! kill -0 "$daemon_pid" 2>/dev/null; then
      break
    fi
    if curl --fail --silent --show-error --max-time 1 \
      --header "Authorization: Bearer $token" \
      --output /dev/null "$host/api/version" 2>/dev/null; then
      ready=1
      break
    fi
    sleep 0.1
    attempt=$((attempt + 1))
  done
  [[ $ready -eq 1 ]] || fail 'daemon did not become ready within 30 seconds'
}

mkdir -p "$data_root" "$work_dir" "$runtime_root" "$home_root"
start_daemon "$active_bootstrap_token"

AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" \
  auth login --token "$active_bootstrap_token" >/dev/null

admin_identity=$(AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" --json auth whoami)
jq -e '.role == "admin" and .token.name == "default-admin" and .authenticationInitialized == true' \
  >/dev/null <<<"$admin_identity" || fail 'bootstrap token is not the initialized default admin'

readonly_create=$(AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" --json \
  auth token create --name readonly-ci --role read-only-admin --description 'read-only CI')
readonly_secret=$(jq -r '.token' <<<"$readonly_create")
readonly_id=$(jq -r '.item.id' <<<"$readonly_create")
[[ $readonly_secret == ac_* && -n $readonly_id ]] || fail 'CLI did not return its locally generated token and item metadata'

AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" \
  auth login --token "$readonly_secret" >/dev/null
readonly_identity=$(AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" --json auth whoami)
jq -e --arg id "$readonly_id" '.role == "read-only-admin" and .token.id == $id' \
  >/dev/null <<<"$readonly_identity" || fail 'read-only token identity is incorrect'

audits_before=$(AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" --json \
  audit ls --token "$readonly_id")
before_count=$(jq '.audits | length' <<<"$audits_before")
AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" status >/dev/null
AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" --json auth token ls >/dev/null
audits_after_queries=$(AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" --json \
  audit ls --token "$readonly_id")
after_query_count=$(jq '.audits | length' <<<"$audits_after_queries")
[[ $after_query_count -eq $before_count ]] || fail 'pure queries unexpectedly created audit rows'

if AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" \
  auth token create --name forbidden --role read-only-admin > /dev/null 2>"$denied_stderr"; then
  fail 'read-only admin created a token'
fi

denied_audits=$(AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" --json \
  audit ls --token "$readonly_id" --action auth.token.create --status denied)
jq -e --arg id "$readonly_id" \
  '.audits | any(.tokenId == $id and .action == "auth.token.create" and .status == "denied")' \
  >/dev/null <<<"$denied_audits" || fail 'denied token creation was not audited'

api_token=$(new_token)
api_request_id=$(new_request_id)
api_request=$(jq -cn \
  --arg name api-client \
  --arg description 'created through Connect JSON' \
  --arg role admin \
  --arg token "$api_token" \
  --arg request_id "$api_request_id" \
  '{name:$name, description:$description, role:$role, token:$token, clientRequestId:$request_id}')
api_response=$(curl --fail --silent --show-error \
  --header 'Connect-Protocol-Version: 1' \
  --header 'Content-Type: application/json' \
  --header "Authorization: Bearer $active_bootstrap_token" \
  --data "$api_request" \
  "$host/agentcompose.v2.AuthService/CreateToken")
jq -e '.created == true and (.item.id | length > 0) and (has("token") | not)' \
  >/dev/null <<<"$api_response" || fail 'CreateToken API response shape is invalid or contains a token field'
if [[ $api_response == *"$api_token"* ]]; then
  fail 'CreateToken API response leaked the plaintext token'
fi

api_retry_response=$(curl --fail --silent --show-error \
  --header 'Connect-Protocol-Version: 1' \
  --header 'Content-Type: application/json' \
  --header "Authorization: Bearer $active_bootstrap_token" \
  --data "$api_request" \
  "$host/agentcompose.v2.AuthService/CreateToken")
jq -e --arg id "$(jq -r '.item.id' <<<"$api_response")" \
  '.created == false and .idempotentReplay == true and .item.id == $id and (has("token") | not)' \
  >/dev/null <<<"$api_retry_response" || fail 'CreateToken idempotent replay did not return the same metadata'

token_list=$(AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" --json auth token ls)
audit_list=$(AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" --json audit ls)
[[ $token_list != *"$readonly_secret"* && $token_list != *"$api_token"* ]] \
  || fail 'token list leaked a plaintext token'
[[ $audit_list != *"$readonly_secret"* && $audit_list != *"$api_token"* ]] \
  || fail 'audit list leaked a plaintext token'

AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" \
  auth token revoke "$readonly_id" >/dev/null
if AGENT_COMPOSE_CONFIG=$readonly_config "$binary" --host "$host" status \
  >/dev/null 2>"$revoked_stderr"; then
  fail 'revoked token still authenticated'
fi

if ! stop_daemon; then
  fail 'daemon did not stop cleanly before environment-token rotation'
fi
active_bootstrap_token=$rotated_bootstrap_token
start_daemon "$active_bootstrap_token"

if AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" status >/dev/null 2>&1; then
  fail 'old environment token still authenticated after rotation'
fi
AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" \
  auth login --token "$active_bootstrap_token" >/dev/null
rotated_identity=$(AGENT_COMPOSE_CONFIG=$admin_config "$binary" --host "$host" --json auth whoami)
jq -e '.role == "admin" and .token.name == "default-admin"' \
  >/dev/null <<<"$rotated_identity" || fail 'rotated environment token is not the default admin'

if ! stop_daemon; then
  fail 'daemon did not stop cleanly'
fi

printf 'agent-compose CLI RBAC, multi-token, revocation, and audit E2E passed\n'
