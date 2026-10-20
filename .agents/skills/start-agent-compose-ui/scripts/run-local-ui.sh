#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/../../../.." && pwd)"
repo_parent="$(dirname "$repo_root")"
sibling_ui_repo="$repo_parent/agent-compose-ui"
tmp_base=/tmp
if [[ "$(uname -s)" == "Darwin" ]]; then
  tmp_base=/private/tmp
fi

root_override=""
engine_port_arg=""
ui_port_arg=""
skip_build=0
open_browser=0

usage() {
  cat <<'USAGE'
Usage: run-local-ui.sh [options]

  --root DIR              Reuse or choose a test root
  --engine-port PORT      Fixed daemon port
  --ui-port PORT          Fixed browser UI port
  --skip-build            Reuse existing daemon and local UI server binaries
  --open                  Open the UI in the default browser
  -h, --help              Show this help

The script uses ../agent-compose-ui when that directory exists; otherwise it
runs the published agent-compose-ui image. Re-running the script refreshes the
single development instance instead of starting another one.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --root) root_override="${2:?missing value for --root}"; shift 2 ;;
    --engine-port) engine_port_arg="${2:?missing value for --engine-port}"; shift 2 ;;
    --ui-port) ui_port_arg="${2:?missing value for --ui-port}"; shift 2 ;;
    --skip-build) skip_build=1; shift ;;
    --open) open_browser=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown option: $1" >&2; usage >&2; exit 2 ;;
  esac
done

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "$1 is required" >&2
    exit 1
  fi
}

require_command docker
require_command curl
require_command openssl
require_command lsof
require_command python3

ui_mode=image
ui_repo=""
vite_js=""
node_bin=""
if [[ -d "$sibling_ui_repo" ]]; then
  ui_repo="$(cd "$sibling_ui_repo" && pwd)"
  if [[ ! -f "$ui_repo/package.json" || ! -f "$ui_repo/cmd/agent-compose-ui-server/main.go" ]]; then
    echo "local UI directory is incomplete: $ui_repo" >&2
    echo "expected package.json and cmd/agent-compose-ui-server/main.go" >&2
    exit 1
  fi
  ui_mode=local
  require_command go
  require_command node
  require_command npm
  node_bin="$(command -v node)"
  vite_js="$ui_repo/node_modules/vite/bin/vite.js"
fi
if [[ "$skip_build" == "0" ]]; then
  require_command task
fi

repo_id="$(printf '%s' "$repo_root" | openssl dgst -sha256 | awk '{print $NF}' | cut -c1-12)"
default_root="$tmp_base/agent-compose-ui-$repo_id"
root="${root_override:-$default_root}"
mkdir -p "$root/data" "$root/ui-data"
root="$(cd "$root" && pwd)"
state_file="$root/state.env"
lock_dir="$root/.start.lock"

ui_env_file="$root/ui.env"
if [[ "$ui_mode" == "local" ]]; then
  ui_env_file="$ui_repo/.env"
fi
local_ui_auth_username=admin
local_ui_auth_password=admin
local_ui_auth_secret=agent-compose-ui-local-development-session

vite_pid=""
ui_server_pid=""
daemon_pid=""
container_name="agent-compose-ui-$repo_id"
container_id=""
vite_repo="$ui_repo"
lock_owned=0
cleaned=0

process_alive() {
  local pid="$1"
  local stat=""
  [[ -n "$pid" ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  stat="$(ps -p "$pid" -o stat= 2>/dev/null | tr -d ' ' || true)"
  [[ -n "$stat" && "$stat" != Z* ]]
}

stop_process() {
  local pid="$1"
  local marker="$2"
  local label="$3"
  local command_line=""
  local i=""

  process_alive "$pid" || return 0
  command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  if [[ -z "$command_line" ]]; then
    sleep 0.1
    process_alive "$pid" || return 0
    command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  fi
  if [[ "$command_line" != *"$marker"* ]]; then
    echo "warning: refusing to stop $label pid $pid; command does not match $marker" >&2
    return 0
  fi
  kill "$pid" 2>/dev/null || true
  for i in $(seq 1 100); do
    process_alive "$pid" || break
    sleep 0.1
  done
  if process_alive "$pid"; then
    kill -KILL "$pid" 2>/dev/null || true
  fi
  wait "$pid" 2>/dev/null || true
}

release_lock() {
  if [[ "$lock_owned" == "1" ]]; then
    if [[ -f "$lock_dir/pid" ]]; then
      rm "$lock_dir/pid"
    fi
    rmdir "$lock_dir" 2>/dev/null || true
    lock_owned=0
  fi
}

cleanup() {
  if [[ "$cleaned" == "1" ]]; then
    return
  fi
  cleaned=1
  stop_process "$vite_pid" "$vite_repo/node_modules/vite/bin/vite.js" "Vite"
  stop_process "$ui_server_pid" "agent-compose-ui-server" "UI server"
  stop_process "$daemon_pid" "agent-compose daemon" "daemon"
  if [[ -n "$container_id" ]]; then
    docker stop "$container_id" >/dev/null 2>&1 || true
    docker rm "$container_id" >/dev/null 2>&1 || true
  fi
  release_lock
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

acquire_lock() {
  local owner=""
  local command_line=""
  if mkdir "$lock_dir" 2>/dev/null; then
    printf '%s\n' "$$" >"$lock_dir/pid"
    lock_owned=1
    return
  fi
  if [[ -f "$lock_dir/pid" ]]; then
    owner="$(cat "$lock_dir/pid" 2>/dev/null || true)"
  fi
  if [[ "$owner" =~ ^[0-9]+$ ]] && kill -0 "$owner" 2>/dev/null; then
    command_line="$(ps -p "$owner" -o command= 2>/dev/null || true)"
  fi
  if [[ "$command_line" == *"start-agent-compose-ui/scripts/run-local-ui.sh"* ]]; then
    echo "another start-agent-compose-ui invocation is preparing the stack (pid $owner)" >&2
    echo "wait for it to finish, then rerun this command" >&2
    exit 4
  fi
  if [[ -f "$lock_dir/pid" ]]; then
    rm "$lock_dir/pid"
  fi
  rmdir "$lock_dir" 2>/dev/null || true
  mkdir "$lock_dir"
  printf '%s\n' "$$" >"$lock_dir/pid"
  lock_owned=1
}

has_llm_key() {
  python3 - "$repo_root/.env" <<'PY'
import re
import sys

path = sys.argv[1]
try:
    with open(path, encoding="utf-8") as handle:
        lines = handle.read().splitlines()
except FileNotFoundError:
    sys.exit(1)

keys = {
    "LLM_API_KEY",
    "OPENAI_API_KEY",
    "ANTHROPIC_API_KEY",
    "ANTHROPIC_AUTH_TOKEN",
}
for raw in lines:
    match = re.match(r"^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$", raw)
    if not match or match.group(1) not in keys:
        continue
    value = match.group(2).strip()
    if not value:
        continue
    if value[0] in "\"'":
        end = value.find(value[0], 1)
        if end != -1:
            value = value[1:end]
    else:
        value = value.split("#", 1)[0].strip()
    if value:
        sys.exit(0)
sys.exit(1)
PY
}

if ! has_llm_key; then
  cat >&2 <<EOF
LLM key is not configured in $repo_root/.env

Add one of the following to the repository .env file, then rerun this skill:
  LLM_API_KEY=...          (preferred generic OpenAI-compatible config)
  OPENAI_API_KEY=...       (OpenAI)
  ANTHROPIC_API_KEY=...    (Anthropic)
  ANTHROPIC_AUTH_TOKEN=... (Anthropic token auth)

For an OpenAI-compatible endpoint, also set:
  LLM_API_ENDPOINT=https://api.openai.com
  LLM_API_PROTOCOL=responses
  LLM_MODEL=your-model

For Anthropic, also set:
  ANTHROPIC_BASE_URL=https://api.anthropic.com
  ANTHROPIC_MODEL=your-model

The .env file is gitignored. Do not paste the key into chat, command output, or
a commit. Ask the user to fill the value and rerun the skill.
EOF
  exit 3
fi

acquire_lock

binary="$repo_root/build/agent-compose"
if [[ "$skip_build" == "0" ]]; then
  (cd "$repo_root" && task build)
fi
if [[ ! -x "$binary" ]]; then
  echo "missing executable: $binary (run task build or omit --skip-build)" >&2
  exit 1
fi

ui_server_binary="$root/bin/agent-compose-ui-server"
if [[ "$ui_mode" == "local" ]]; then
  if [[ ! -d "$ui_repo/node_modules" || "$ui_repo/package-lock.json" -nt "$ui_repo/node_modules/.package-lock.json" ]]; then
    (cd "$ui_repo" && npm ci)
  fi
  if [[ ! -f "$vite_js" ]]; then
    echo "missing Vite entrypoint: $vite_js" >&2
    echo "run npm ci in $ui_repo" >&2
    exit 1
  fi
  if [[ "$skip_build" == "0" ]]; then
    mkdir -p "$root/bin"
    (cd "$ui_repo" && go build -o "$ui_server_binary" ./cmd/agent-compose-ui-server)
  fi
  if [[ ! -x "$ui_server_binary" ]]; then
    echo "missing UI server executable: $ui_server_binary (omit --skip-build)" >&2
    exit 1
  fi
fi

ui_image="${AGENT_COMPOSE_UI_IMAGE:-chaitin/agent-compose-ui:latest}"
guest_image="${DEFAULT_IMAGE:-chaitin/agent-compose-guest:latest}"
if [[ "$ui_mode" == "image" ]]; then
  if ! docker pull "$ui_image"; then
    if ! docker image inspect "$ui_image" >/dev/null 2>&1; then
      echo "cannot pull $ui_image and no local copy is available" >&2
      exit 1
    fi
    echo "warning: unable to refresh $ui_image; using the local copy" >&2
  fi
fi

state_value() {
  sed -n "s/^$1=//p" "$state_file" 2>/dev/null | tail -n 1
}

old_ui_mode=""
old_engine_port=""
old_ui_port=""
old_ui_server_port=""
old_ui_repo=""
old_supervisor_pid=""
old_daemon_pid=""
old_ui_server_pid=""
old_vite_pid=""
old_container_name=""
old_container_id=""

if [[ -f "$state_file" ]]; then
  old_ui_mode="$(state_value STATE_UI_MODE)"
  old_engine_port="$(state_value STATE_ENGINE_PORT)"
  old_ui_port="$(state_value STATE_UI_PORT)"
  old_ui_server_port="$(state_value STATE_UI_SERVER_PORT)"
  old_ui_repo="$(state_value STATE_UI_REPO)"
  old_supervisor_pid="$(state_value STATE_SUPERVISOR_PID)"
  old_daemon_pid="$(state_value STATE_DAEMON_PID)"
  old_ui_server_pid="$(state_value STATE_UI_SERVER_PID)"
  old_vite_pid="$(state_value STATE_VITE_PID)"
  old_container_name="$(state_value STATE_CONTAINER_NAME)"
  old_container_id="$(state_value STATE_CONTAINER_ID)"
fi

if [[ -n "$old_ui_repo" ]]; then
  vite_repo="$old_ui_repo"
fi
stop_process "$old_supervisor_pid" "start-agent-compose-ui/scripts/run-local-ui.sh" "previous launcher"
stop_process "$old_vite_pid" "$vite_repo/node_modules/vite/bin/vite.js" "Vite"
stop_process "$old_ui_server_pid" "agent-compose-ui-server" "UI server"
stop_process "$old_daemon_pid" "agent-compose daemon" "daemon"
if [[ -n "$old_container_id" ]]; then
  docker stop "$old_container_id" >/dev/null 2>&1 || true
  docker rm "$old_container_id" >/dev/null 2>&1 || true
elif [[ -n "$old_container_name" ]]; then
  docker stop "$old_container_name" >/dev/null 2>&1 || true
  docker rm "$old_container_name" >/dev/null 2>&1 || true
fi
if [[ -f "$state_file" ]]; then
  rm "$state_file"
fi
vite_repo="$ui_repo"

port_in_use() {
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}

free_port() {
  local candidate="$1"
  while port_in_use "$candidate"; do
    candidate=$((candidate + 1))
  done
  printf '%s\n' "$candidate"
}

if [[ -n "$engine_port_arg" ]]; then
  if port_in_use "$engine_port_arg"; then
    echo "engine port $engine_port_arg is already in use" >&2
    exit 1
  fi
  engine_port="$engine_port_arg"
else
  engine_port="$(free_port "${old_engine_port:-7411}")"
fi

if [[ -n "$ui_port_arg" ]]; then
  if [[ "$ui_port_arg" == "$engine_port" ]]; then
    echo "engine and UI ports must differ" >&2
    exit 1
  fi
  if port_in_use "$ui_port_arg"; then
    echo "UI port $ui_port_arg is already in use" >&2
    exit 1
  fi
  ui_port="$ui_port_arg"
else
  ui_port="$(free_port "${old_ui_port:-18081}")"
  while [[ "$ui_port" == "$engine_port" ]]; do
    ui_port=$((ui_port + 1))
    ui_port="$(free_port "$ui_port")"
  done
fi

ui_server_port=""
if [[ "$ui_mode" == "local" ]]; then
  ui_server_port="$(free_port "${old_ui_server_port:-18082}")"
  while [[ "$ui_server_port" == "$engine_port" || "$ui_server_port" == "$ui_port" ]]; do
    ui_server_port=$((ui_server_port + 1))
    ui_server_port="$(free_port "$ui_server_port")"
  done
fi

ensure_ui_env() {
  local tmp=""
  if [[ -s "$ui_env_file" ]] &&
    grep -q '^AUTH_USERNAME=.' "$ui_env_file" &&
    grep -q '^AUTH_PASSWORD=.' "$ui_env_file" &&
    grep -q '^AUTH_SECRET=.' "$ui_env_file"; then
    chmod 600 "$ui_env_file"
    return
  fi
  if [[ -e "$ui_env_file" ]]; then
    echo "UI env file is incomplete: $ui_env_file" >&2
    echo "set AUTH_USERNAME, AUTH_PASSWORD, and AUTH_SECRET, or remove the file" >&2
    exit 1
  fi
  mkdir -p "$(dirname "$ui_env_file")"
  tmp="$ui_env_file.tmp.$$"
  umask 077
  {
    echo "# Local UI login for agent-compose development. Do not use in production."
    echo "AUTH_USERNAME=$local_ui_auth_username"
    echo "AUTH_PASSWORD=$local_ui_auth_password"
    echo "AUTH_SECRET=$local_ui_auth_secret"
    echo "AUTH_SESSION_TTL=24h"
  } >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$ui_env_file"
}

ensure_ui_env

load_ui_env() {
  local marker=""
  {
    IFS= read -r -d '' AUTH_USERNAME || true
    IFS= read -r -d '' AUTH_PASSWORD || true
    IFS= read -r -d '' AUTH_SECRET || true
    IFS= read -r -d '' AUTH_SESSION_TTL || true
    IFS= read -r -d '' marker || true
    [[ "$marker" == "complete" ]]
  } < <(python3 - "$ui_env_file" <<'PY'
import re
import sys

path = sys.argv[1]
keys = ["AUTH_USERNAME", "AUTH_PASSWORD", "AUTH_SECRET", "AUTH_SESSION_TTL"]
required = keys[:3]
values = dict()


def parse_value(raw):
    value = raw.lstrip()
    if not value:
        return ""
    if value.startswith('"'):
        result = []
        escaped = False
        for index in range(1, len(value)):
            char = value[index]
            if escaped:
                if char == "n":
                    char = "\n"
                elif char == "r":
                    char = "\r"
                elif char == "t":
                    char = "\t"
                result.append(char)
                escaped = False
            elif char == "\\":
                escaped = True
            elif char == '"':
                remainder = value[index + 1:].strip()
                if remainder and not remainder.startswith("#"):
                    raise ValueError("unexpected content after double-quoted value")
                return "".join(result)
            else:
                result.append(char)
        raise ValueError("unterminated double-quoted value")
    if value.startswith("'"):
        end = value.find("'", 1)
        if end == -1:
            raise ValueError("unterminated single-quoted value")
        remainder = value[end + 1:].strip()
        if remainder and not remainder.startswith("#"):
            raise ValueError("unexpected content after single-quoted value")
        return value[1:end]
    return re.sub(r"\s+#.*$", "", value).rstrip()


try:
    with open(path, encoding="utf-8") as handle:
        for raw in handle:
            match = re.match(r"^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*$", raw)
            if not match or match.group(1) not in keys:
                continue
            values[match.group(1)] = parse_value(match.group(2))
except (OSError, ValueError) as exc:
    print("invalid UI env file %s: %s" % (path, exc), file=sys.stderr)
    sys.exit(2)

missing = [key for key in required if not values.get(key)]
if missing:
    print("UI env file is missing non-empty values for: %s" % ", ".join(missing), file=sys.stderr)
    sys.exit(2)
for key in keys:
    sys.stdout.buffer.write(values.get(key, "").encode("utf-8") + b"\0")
sys.stdout.buffer.write(b"complete\0")
PY
  ) || {
    echo "failed to parse UI env file: $ui_env_file" >&2
    exit 1
  }
}

load_ui_env

can_bind_tcp_host() {
  python3 - "$1" "$2" <<'PY'
import socket
import sys

host = sys.argv[1]
port = int(sys.argv[2])
with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
    sock.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    try:
        sock.bind((host, port))
    except OSError:
        sys.exit(1)
PY
}

daemon_listen_host=127.0.0.1
daemon_connect_host=127.0.0.1
container_daemon_host=host.docker.internal
if [[ "$(uname -s)" != "Darwin" ]]; then
  docker_gateway="$(docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}' 2>/dev/null || true)"
  if [[ -n "$docker_gateway" && "$docker_gateway" != "<no value>" ]] && can_bind_tcp_host "$docker_gateway" "$engine_port"; then
    daemon_listen_host="$docker_gateway"
    daemon_connect_host="$docker_gateway"
    container_daemon_host="$docker_gateway"
  else
    docker_os="$(docker info --format '{{.OperatingSystem}}' 2>/dev/null || true)"
    if [[ "$docker_os" != *"Docker Desktop"* ]]; then
      cat >&2 <<EOF
cannot determine a Docker-reachable daemon address for engine port $engine_port
Docker bridge gateway ${docker_gateway:-<unknown>} is not assigned to this host,
and the active Docker engine is not Docker Desktop. Use Docker Desktop or a
rootful Docker bridge network for this local UI workflow.
EOF
      exit 1
    fi
  fi
fi
daemon_url="http://$daemon_connect_host:$engine_port"

daemon_log="$root/daemon.log"
ui_server_log="$root/ui-server.log"
ui_log="$root/ui.log"

export DATA_ROOT="$root/data"
export HTTP_LISTEN="$daemon_listen_host:$engine_port"
export AGENT_COMPOSE_SOCKET="$root/agent-compose.sock"
export DEFAULT_IMAGE="$guest_image"
export AGENT_COMPOSE_RUNTIME_BASE_URL="http://$container_daemon_host:$engine_port"

(
  cd "$repo_root"
  exec "$binary" daemon
) >"$daemon_log" 2>&1 &
daemon_pid=$!

if [[ "$ui_mode" == "local" ]]; then
  (
    cd "$ui_repo"
    export AUTH_USERNAME AUTH_PASSWORD AUTH_SECRET AUTH_SESSION_TTL
    export AGENT_COMPOSE_UI_LISTEN="127.0.0.1:$ui_server_port"
    export AGENT_COMPOSE_BACKEND="$daemon_url"
    export UI_DATABASE_PATH="$root/ui-data/agent-compose-ui.db"
    export SANDBOX_ROOT="$root/data/sandboxes"
    exec "$ui_server_binary"
  ) >"$ui_server_log" 2>&1 &
  ui_server_pid=$!

  (
    cd "$ui_repo"
    export AGENT_COMPOSE_DEV_UI_SERVER="http://127.0.0.1:$ui_server_port"
    exec "$node_bin" "$vite_js" --host 127.0.0.1 --port "$ui_port" --strictPort
  ) >"$ui_log" 2>&1 &
  vite_pid=$!
else
  if docker inspect "$container_name" >/dev/null 2>&1; then
    docker stop "$container_name" >/dev/null 2>&1 || true
    docker rm "$container_name" >/dev/null 2>&1 || true
  fi
  container_id="$(docker run -d \
    --name "$container_name" \
    --add-host host.docker.internal:host-gateway \
    -p "127.0.0.1:$ui_port:8000" \
    --env-file "$ui_env_file" \
    -e UI_DATABASE_PATH=/data/agent-compose-ui.db \
    -e AGENT_COMPOSE_BACKEND="http://$container_daemon_host:$engine_port" \
    -e JUPYTER_PROXY_BASE=/jupyter \
    -e NGINX_JUPYTER_PROXY_BASE=/jupyter \
    -e SANDBOX_ROOT=/data/sandboxes \
    -v "$root/ui-data":/data \
    -v "$root/data/sandboxes":/data/sandboxes:ro \
    "$ui_image")"
fi

state_tmp="$state_file.tmp.$$"
{
  echo "STATE_UI_MODE=$ui_mode"
  echo "STATE_ENGINE_PORT=$engine_port"
  echo "STATE_UI_PORT=$ui_port"
  echo "STATE_UI_SERVER_PORT=$ui_server_port"
  echo "STATE_UI_REPO=$ui_repo"
  echo "STATE_UI_ENV_FILE=$ui_env_file"
  echo "STATE_DAEMON_URL=$daemon_url"
  echo "STATE_SUPERVISOR_PID=$$"
  echo "STATE_DAEMON_PID=$daemon_pid"
  echo "STATE_UI_SERVER_PID=$ui_server_pid"
  echo "STATE_VITE_PID=$vite_pid"
  echo "STATE_CONTAINER_NAME=$container_name"
  echo "STATE_CONTAINER_ID=$container_id"
} >"$state_tmp"
chmod 600 "$state_tmp"
mv "$state_tmp" "$state_file"
release_lock

wait_for_url() {
  local url="$1"
  local label="$2"
  local attempt=""
  for attempt in $(seq 1 150); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.2
  done
  echo "$label did not become healthy: $url" >&2
  return 1
}

if ! wait_for_url "$daemon_url/api/version" "daemon"; then
  tail -n 60 "$daemon_log" >&2 || true
  exit 1
fi
if ! wait_for_url "http://127.0.0.1:$ui_port/" "UI"; then
  if [[ "$ui_mode" == "local" ]]; then
    tail -n 60 "$ui_log" >&2 || true
    tail -n 60 "$ui_server_log" >&2 || true
  else
    docker logs "$container_id" >&2 || true
  fi
  exit 1
fi

login_payload="$(AUTH_USERNAME="$AUTH_USERNAME" AUTH_PASSWORD="$AUTH_PASSWORD" python3 -c 'import json,os; print(json.dumps({"username": os.environ["AUTH_USERNAME"], "password": os.environ["AUTH_PASSWORD"]}))')"
logged_in=0
for _ in $(seq 1 100); do
  if curl -fsS -c "$root/cookies.txt" \
    -H 'Content-Type: application/json' \
    --data "$login_payload" \
    "http://127.0.0.1:$ui_port/api/auth/login" >/dev/null 2>&1; then
    logged_in=1
    break
  fi
  sleep 0.2
done
if [[ "$logged_in" != "1" ]]; then
  echo "UI login probe failed" >&2
  if [[ "$ui_mode" == "local" ]]; then
    tail -n 60 "$ui_log" >&2 || true
    tail -n 60 "$ui_server_log" >&2 || true
  else
    docker logs "$container_id" >&2 || true
  fi
  exit 1
fi
if ! curl -fsS -b "$root/cookies.txt" \
  "http://127.0.0.1:$ui_port/api/version" >/dev/null; then
  echo "UI-to-daemon proxy probe failed" >&2
  exit 1
fi

cat >"$root/runbook.txt" <<RUNBOOK
agent-compose local development stack

URL: http://localhost:$ui_port
Username: $AUTH_USERNAME
Password: $AUTH_PASSWORD
UI env: $ui_env_file

Daemon: $daemon_url
Data: $root/data
UI data: $root/ui-data
Daemon log: $daemon_log
UI mode: $ui_mode
UI source: $ui_repo
UI server log: $ui_server_log
Vite log: $ui_log
Refresh: rerun the same skill command
Stop: Ctrl-C in the foreground script
RUNBOOK
chmod 600 "$root/runbook.txt"

echo "agent-compose UI ready"
echo "URL: http://localhost:$ui_port"
echo "Username: $AUTH_USERNAME"
echo "Password: $AUTH_PASSWORD"
echo "UI env: $ui_env_file"
echo "Daemon: $daemon_url"
echo "Daemon log: $daemon_log"
if [[ "$ui_mode" == "local" ]]; then
  echo "UI: local source $ui_repo (Vite HMR enabled)"
  echo "UI server log: $ui_server_log"
  echo "Vite log: $ui_log"
else
  echo "UI: $ui_image (container $container_name)"
fi
echo "Re-run the same command to rebuild and refresh this instance."
echo "Press Ctrl-C to stop the stack."

if [[ "$open_browser" == "1" ]]; then
  if command -v open >/dev/null 2>&1; then
    open "http://localhost:$ui_port" >/dev/null 2>&1 || true
  elif command -v xdg-open >/dev/null 2>&1; then
    xdg-open "http://localhost:$ui_port" >/dev/null 2>&1 || true
  fi
fi

while :; do
  if ! process_alive "$daemon_pid"; then
    echo "daemon exited; see $daemon_log" >&2
    exit 1
  fi
  if [[ "$ui_mode" == "local" ]]; then
    if ! process_alive "$ui_server_pid"; then
      echo "UI server exited; see $ui_server_log" >&2
      exit 1
    fi
    if ! process_alive "$vite_pid"; then
      echo "Vite exited; see $ui_log" >&2
      exit 1
    fi
  elif [[ "$(docker inspect -f '{{.State.Running}}' "$container_id" 2>/dev/null || true)" != "true" ]]; then
    echo "UI container exited: $container_name" >&2
    exit 1
  fi
  sleep 1
done
