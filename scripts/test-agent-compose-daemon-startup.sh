#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: test-agent-compose-daemon-startup.sh \
  --binary PATH \
  --expected-os OS \
  --expected-arch ARCH \
  --expected-drivers DRIVER[,DRIVER...]

Start an agent-compose daemon in an isolated temporary environment, query its
/api/version endpoint through a Unix socket, and verify its build metadata.
The smoke does not require a Docker daemon or KVM.
EOF
}

fail() {
  printf 'test-agent-compose-daemon-startup: %s\n' "$*" >&2
  exit 1
}

binary=''
expected_os=''
expected_arch=''
expected_drivers=''

while [[ $# -gt 0 ]]; do
  case $1 in
    --binary)
      [[ $# -ge 2 ]] || fail '--binary requires a value'
      binary=$2
      shift 2
      ;;
    --expected-os)
      [[ $# -ge 2 ]] || fail '--expected-os requires a value'
      expected_os=$2
      shift 2
      ;;
    --expected-arch)
      [[ $# -ge 2 ]] || fail '--expected-arch requires a value'
      expected_arch=$2
      shift 2
      ;;
    --expected-drivers)
      [[ $# -ge 2 ]] || fail '--expected-drivers requires a value'
      expected_drivers=$2
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
[[ -n $expected_os ]] || fail '--expected-os is required'
[[ -n $expected_arch ]] || fail '--expected-arch is required'
[[ -n $expected_drivers ]] || fail '--expected-drivers is required'
case $expected_drivers in
  ,*|*,|*,,*) fail '--expected-drivers must be a non-empty comma-separated list' ;;
esac

for command_name in curl jq mktemp; do
  command -v "$command_name" >/dev/null 2>&1 \
    || fail "$command_name is required"
done

case $binary in
  /*) ;;
  *) binary=$PWD/$binary ;;
esac
binary_dir=$(cd -P "$(dirname "$binary")" && pwd)
binary=$binary_dir/$(basename "$binary")
[[ -f $binary && -x $binary ]] || fail "binary is not an executable regular file: $binary"

test_root=$(mktemp -d "${TMPDIR:-/tmp}/ac-daemon-smoke.XXXXXX")
work_dir=$test_root/work
data_root=$test_root/data
home_root=$test_root/home
runtime_root=$test_root/runtime
socket_path=$test_root/a.sock
stdout_log=$test_root/daemon.stdout
stderr_log=$test_root/daemon.stderr
curl_log=$test_root/curl.stderr
response_json=$test_root/version.json
cli_json=$test_root/cli-version.json
cli_stderr=$test_root/cli-version.stderr
daemon_pid=''

print_diagnostic_file() {
  local label=$1 file=$2
  if [[ -s $file ]]; then
    printf '\n--- %s ---\n' "$label" >&2
    sed 's/^/  /' "$file" >&2
  fi
}

stop_daemon() {
  local pid exit_status=0 forced=0 attempt=0
  [[ -n $daemon_pid ]] || return 0
  pid=$daemon_pid

  if kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    while kill -0 "$pid" 2>/dev/null && [[ $attempt -lt 50 ]]; do
      sleep 0.1
      attempt=$((attempt + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
      forced=1
      kill -KILL "$pid" 2>/dev/null || true
    fi
  fi

  if wait "$pid"; then
    exit_status=0
  else
    exit_status=$?
  fi
  daemon_pid=''
  [[ $forced -eq 0 && $exit_status -eq 0 ]]
}

cleanup() {
  local status=$?
  trap - EXIT HUP INT TERM
  if [[ -n $daemon_pid ]]; then
    stop_daemon || true
  fi
  if [[ $status -ne 0 ]]; then
    print_diagnostic_file 'binary --json version stderr' "$cli_stderr"
    print_diagnostic_file 'binary --json version output' "$cli_json"
    print_diagnostic_file 'daemon stdout' "$stdout_log"
    print_diagnostic_file 'daemon stderr' "$stderr_log"
    print_diagnostic_file 'last curl error' "$curl_log"
    print_diagnostic_file '/api/version response' "$response_json"
  fi
  rm -rf "$test_root"
  exit "$status"
}

interrupted() {
  printf 'test-agent-compose-daemon-startup: interrupted\n' >&2
  exit 130
}

trap cleanup EXIT
trap interrupted HUP INT TERM

mkdir -p "$work_dir" "$data_root" "$home_root" "$runtime_root"

(
  cd "$work_dir"
  "$binary" --json version
) >"$cli_json" 2>"$cli_stderr" \
  || fail 'binary --json version failed'

jq -e \
  --arg os "$expected_os" \
  --arg arch "$expected_arch" \
  --arg drivers "$expected_drivers" \
  'keys == ["arch", "compiled_drivers", "os", "version"] and
   .os == $os and
   .arch == $arch and
   .compiled_drivers == ($drivers | split(",")) and
   (.version | type == "string" and length > 0)' \
  "$cli_json" >/dev/null \
  || fail 'binary --json version metadata does not match the expected build'

expected_version=$(jq -r '.version' "$cli_json")

(
  cd "$work_dir"
  unset \
    AGENT_COMPOSE_HOST \
    COMPOSE_DISABLE_ENV_FILE COMPOSE_ENV_FILES COMPOSE_FILE COMPOSE_PATH_SEPARATOR \
    COMPOSE_PROFILES COMPOSE_PROJECT_NAME \
    DOCKER_CERT_PATH DOCKER_CONFIG DOCKER_CONTEXT DOCKER_TLS_VERIFY \
    HTTP_BASIC_AUTH \
    LLM_API_ENDPOINT LLM_API_KEY OPENAI_API_KEY
  export AGENT_COMPOSE_SOCKET="$socket_path"
  export AUTH_PASSWORD=''
  export AUTH_USERNAME=''
  export DATA_ROOT="$data_root"
  export DOCKER_HOST="unix://$test_root/docker-unavailable.sock"
  export HOME="$home_root"
  export HTTP_LISTEN=''
  export RUNTIME_DRIVER=docker
  export SANDBOX_ROOT="$data_root/sandboxes"
  export XDG_RUNTIME_DIR="$runtime_root"
  exec "$binary" daemon
) >"$stdout_log" 2>"$stderr_log" &
daemon_pid=$!

ready=0
attempt=0
while [[ $attempt -lt 300 ]]; do
  if ! kill -0 "$daemon_pid" 2>/dev/null; then
    break
  fi
  if curl \
    --fail \
    --silent \
    --show-error \
    --max-time 1 \
    --unix-socket "$socket_path" \
    --output "$response_json" \
    http://localhost/api/version 2>"$curl_log"; then
    ready=1
    break
  fi
  sleep 0.1
  attempt=$((attempt + 1))
done
[[ $ready -eq 1 ]] || fail 'daemon did not serve /api/version within 30 seconds'

jq -e \
  --arg version "$expected_version" \
  --arg os "$expected_os" \
  --arg arch "$expected_arch" \
  --arg drivers "$expected_drivers" \
  'keys == ["data", "err", "msg"] and
   .err == null and
   .msg == "OK" and
   (.data | keys) == ["arch", "compiled_drivers", "os", "timestamp", "timezone", "timezone_offset", "version"] and
   .data.version == $version and
   .data.os == $os and
   .data.arch == $arch and
   .data.compiled_drivers == ($drivers | split(",")) and
   (.data.timestamp | type == "number" and . > 0) and
   (.data.timezone | type == "string" and length > 0) and
   (.data.timezone_offset | type == "number")' \
  "$response_json" >/dev/null \
  || fail '/api/version metadata does not match the expected build'

if ! stop_daemon; then
  fail 'daemon did not terminate cleanly after SIGTERM'
fi

printf 'Daemon startup smoke passed: %s/%s [%s]\n' \
  "$expected_os" "$expected_arch" "$expected_drivers"
