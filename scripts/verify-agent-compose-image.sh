#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
usage: verify-agent-compose-image.sh --image IMAGE --arch GOARCH

Verify the platform, default environment, build metadata, native runtime
artifacts, and default unprivileged/no-device behavior of a full
agent-compose daemon image. Docker honors DOCKER_HOST and related client
configuration from the caller's environment.
EOF
}

die() {
  printf 'verify-agent-compose-image: %s\n' "$*" >&2
  exit 1
}

image=''
expected_arch=''

while [[ $# -gt 0 ]]; do
  case $1 in
    --image)
      [[ $# -ge 2 ]] || die '--image requires a value'
      image=$2
      shift 2
      ;;
    --arch)
      [[ $# -ge 2 ]] || die '--arch requires a value'
      expected_arch=$2
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n $image ]] || die '--image is required'
[[ -n $expected_arch ]] || die '--arch is required'
case $expected_arch in
  amd64|arm64) ;;
  *) die '--arch must be amd64 or arm64' ;;
esac
command -v docker >/dev/null 2>&1 || die 'docker is required'
command -v jq >/dev/null 2>&1 || die 'jq is required'

run_id=${VERIFY_AGENT_COMPOSE_IMAGE_RUN_ID:-"$(date +%s)-$$-$RANDOM"}
case $run_id in
  *[!a-zA-Z0-9_.-]*) die 'VERIFY_AGENT_COMPOSE_IMAGE_RUN_ID contains invalid container-name characters' ;;
esac
version_container="ac-image-verify-$run_id-version"
runtime_container="ac-image-verify-$run_id-runtime"
version_active=0
runtime_active=0

cleanup() {
  local status=$? cleanup_status=0
  trap - EXIT HUP INT TERM
  if [[ $runtime_active -eq 1 ]]; then
    docker rm -f "$runtime_container" >/dev/null 2>&1 || cleanup_status=1
  fi
  if [[ $version_active -eq 1 ]]; then
    docker rm -f "$version_container" >/dev/null 2>&1 || cleanup_status=1
  fi
  if [[ $status -eq 0 && $cleanup_status -ne 0 ]]; then
    printf 'verify-agent-compose-image: failed to remove verifier containers\n' >&2
    status=1
  fi
  exit "$status"
}

interrupted() {
  printf 'verify-agent-compose-image: interrupted\n' >&2
  exit 130
}

trap cleanup EXIT
trap interrupted HUP INT TERM

if ! image_metadata=$(docker image inspect --format '{{json .}}' "$image"); then
  die "cannot inspect image: $image"
fi

jq -e \
  --arg arch "$expected_arch" \
  'def image_env:
     reduce (.Config.Env // [])[] as $entry ({};
       ($entry | index("=")) as $separator |
       if $separator == null then .
       else . + {($entry[0:$separator]): $entry[$separator + 1:]}
       end);
   .Os == "linux" and
   .Architecture == $arch and
   (image_env.RUNTIME_DRIVER == "docker") and
   (image_env.BOXLITE_RUNTIME_DIR == "/app/boxlite/runtime") and
   (image_env.MICROSANDBOX_HOME == "/data/microsandbox") and
   (image_env.MICROSANDBOX_MSB_PATH == "/app/microsandbox/bin/msb") and
   (image_env.MICROSANDBOX_LIB_PATH == "/app/microsandbox/lib/libmicrosandbox_go_ffi.so") and
   (image_env.LD_LIBRARY_PATH == "/app/boxlite/runtime:/app/microsandbox/lib")' \
  <<<"$image_metadata" >/dev/null \
  || die "image platform or default runtime environment mismatch: $image"

if ! docker create \
  --name "$version_container" \
  --label "agent-compose.verify-image.run=$run_id" \
  --entrypoint /app/agent-compose \
  "$image" --json version >/dev/null; then
  die 'cannot create agent-compose version verifier container'
fi
version_active=1

if ! version_metadata=$(docker start --attach "$version_container"); then
  die 'agent-compose --json version failed in image'
fi
jq -e \
  --arg arch "$expected_arch" \
  'keys == ["arch", "compiled_drivers", "os", "version"] and
   .os == "linux" and
   .arch == $arch and
   .compiled_drivers == ["docker", "boxlite", "microsandbox"] and
   (.version | type == "string" and length > 0)' \
  <<<"$version_metadata" >/dev/null \
  || die 'agent-compose image build metadata mismatch'

docker rm -f "$version_container" >/dev/null
version_active=0

runtime_check='set -eu
test -x /app/agent-compose
test -x /app/boxlite/runtime/boxlite-guest
test -x /app/boxlite/runtime/boxlite-shim
test -x /app/boxlite/runtime/bwrap
test -x /app/boxlite/runtime/debugfs
test -x /app/boxlite/runtime/mke2fs
test -s /app/boxlite/runtime/libkrunfw.so.5
test -x /app/microsandbox/bin/msb
test -x /app/microsandbox/bin/agentd
test -s /app/microsandbox/lib/libmicrosandbox_go_ffi.so
test -s /app/microsandbox/lib/libkrunfw.so
command -v qemu-img >/dev/null
mkfs.ext4 2>&1 | grep -Fq -- "[-d root-directory"
test ! -e /dev/kvm'

if ! docker create \
  --name "$runtime_container" \
  --label "agent-compose.verify-image.run=$run_id" \
  --entrypoint /bin/sh \
  "$image" -ec "$runtime_check" >/dev/null; then
  die 'cannot create agent-compose runtime verifier container'
fi
runtime_active=1

if ! host_config=$(docker container inspect --format '{{json .HostConfig}}' "$runtime_container"); then
  die 'cannot inspect verifier runtime container security settings'
fi
jq -e \
  '.Privileged == false and
   ((.Devices // []) | length == 0) and
   ((.DeviceRequests // []) | length == 0)' \
  <<<"$host_config" >/dev/null \
  || die 'verifier runtime container is privileged or has host devices'

docker start --attach "$runtime_container" >/dev/null \
  || die 'required runtime artifact or no-KVM check failed in image'
docker rm -f "$runtime_container" >/dev/null
runtime_active=0

printf 'Verified agent-compose image %s: linux/%s, docker default, full runtime artifacts, no KVM\n' \
  "$image" "$expected_arch"
