#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
VERIFIER="$ROOT_DIR/scripts/verify-agent-compose-binary.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

fail() {
  printf 'test-verify-agent-compose-binary: %s\n' "$*" >&2
  exit 1
}

FAKE_BINARY="$TEST_ROOT/fake agent-compose"
cat >"$FAKE_BINARY" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 2 && $1 == --json && $2 == version ]] || exit 91
[[ ${FAKE_BINARY_FAIL:-0} != 1 ]] || exit 92
printf '%s\n' "${FAKE_BINARY_JSON:-}"
EOF
chmod 0755 "$FAKE_BINARY"

RUN_STDOUT="$TEST_ROOT/stdout"
RUN_STDERR="$TEST_ROOT/stderr"
RUN_STATUS=0

run_verifier() {
  set +e
  "$VERIFIER" "$@" >"$RUN_STDOUT" 2>"$RUN_STDERR"
  RUN_STATUS=$?
  set -e
}

assert_success() {
  [[ $RUN_STATUS -eq 0 ]] || {
    sed 's/^/stdout: /' "$RUN_STDOUT" >&2
    sed 's/^/stderr: /' "$RUN_STDERR" >&2
    fail "status=$RUN_STATUS, expected success"
  }
}

assert_failure() {
  [[ $RUN_STATUS -ne 0 ]] || fail 'verifier unexpectedly succeeded'
}

assert_stderr() {
  grep -F -- "$1" "$RUN_STDERR" >/dev/null \
    || fail "stderr lacks expected text: $1"
}

linux_full='{"version":"v-test","os":"linux","arch":"amd64","compiled_drivers":["docker","boxlite","microsandbox"]}'
FAKE_BINARY_JSON=$linux_full run_verifier \
  --binary "$FAKE_BINARY" \
  --os linux \
  --arch amd64 \
  --drivers docker,boxlite,microsandbox
assert_success
grep -F 'compiled_drivers=docker,boxlite,microsandbox' "$RUN_STDOUT" >/dev/null \
  || fail 'success output lacks verified driver list'

FAKE_BINARY_JSON=$linux_full run_verifier \
  --binary "$FAKE_BINARY" --os darwin --arch amd64 --drivers docker,boxlite,microsandbox
assert_failure
assert_stderr 'metadata mismatch'
assert_stderr '"os":"darwin"'

FAKE_BINARY_JSON=$linux_full run_verifier \
  --binary "$FAKE_BINARY" --os linux --arch arm64 --drivers docker,boxlite,microsandbox
assert_failure
assert_stderr 'metadata mismatch'

FAKE_BINARY_JSON=$linux_full run_verifier \
  --binary "$FAKE_BINARY" --os linux --arch amd64 --drivers docker,microsandbox,boxlite
assert_failure
assert_stderr 'metadata mismatch'

FAKE_BINARY_JSON='{"version":"","os":"linux","arch":"amd64","compiled_drivers":["docker","boxlite","microsandbox"]}' \
  run_verifier --binary "$FAKE_BINARY" --os linux --arch amd64 --drivers docker,boxlite,microsandbox
assert_failure
assert_stderr 'metadata mismatch'

FAKE_BINARY_JSON='not-json' run_verifier \
  --binary "$FAKE_BINARY" --os linux --arch amd64 --drivers docker
assert_failure
assert_stderr 'invalid JSON metadata'

FAKE_BINARY_JSON=$'{"version":"v1","os":"linux","arch":"amd64","compiled_drivers":["docker"]}\n{"version":"v2","os":"linux","arch":"amd64","compiled_drivers":["docker"]}' \
  run_verifier --binary "$FAKE_BINARY" --os linux --arch amd64 --drivers docker
assert_failure
assert_stderr 'exactly one JSON metadata object'

FAKE_BINARY_FAIL=1 run_verifier \
  --binary "$FAKE_BINARY" --os linux --arch amd64 --drivers docker
assert_failure
assert_stderr 'binary command failed'

FAKE_BINARY_JSON=$linux_full run_verifier \
  --binary "$FAKE_BINARY" --os linux --arch amd64 --drivers 'docker, boxlite'
assert_failure
assert_stderr 'comma-separated list'

run_verifier --binary "$FAKE_BINARY" --os linux --arch amd64
assert_failure
assert_stderr '--drivers is required'

printf 'test-verify-agent-compose-binary: all checks passed\n'
