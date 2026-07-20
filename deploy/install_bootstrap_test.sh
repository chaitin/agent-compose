#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
INSTALL_SH="$ROOT_DIR/deploy/install.sh"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

fail() {
  printf 'install_bootstrap_test: %s\n' "$*" >&2
  exit 1
}

FAKE_RELEASE="$TEST_ROOT/release"
FAKE_BIN="$TEST_ROOT/bin"
CURL_LOG="$TEST_ROOT/curl.log"
EXEC_LOG="$TEST_ROOT/exec.log"
mkdir -p "$FAKE_RELEASE" "$FAKE_BIN"

for arch in amd64 arm64; do
  asset="agent-compose-installer-linux-$arch"
  cat >"$FAKE_RELEASE/$asset" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >"$EXEC_LOG"
EOF
  chmod 0755 "$FAKE_RELEASE/$asset"
done
(
  cd "$FAKE_RELEASE"
  sha256sum agent-compose-installer-linux-amd64 agent-compose-installer-linux-arm64 >SHASUMS256.txt
)

cat >"$FAKE_BIN/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
output=
url=
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) output=$2; shift 2 ;;
    -*) shift ;;
    *) url=$1; shift ;;
  esac
done
printf '%s\n' "$url" >>"$CURL_LOG"
cp "$FAKE_RELEASE/${url##*/}" "$output"
EOF
chmod 0755 "$FAKE_BIN/curl"

run_bootstrap() {
  local machine=$1
  shift
  : >"$CURL_LOG"
	PATH="$FAKE_BIN:$PATH" \
	FAKE_RELEASE="$FAKE_RELEASE" CURL_LOG="$CURL_LOG" EXEC_LOG="$EXEC_LOG" \
	AGENT_COMPOSE_UNAME_S=Linux AGENT_COMPOSE_UNAME_M="$machine" \
	AGENT_COMPOSE_INSTALLER_BASE_URL=https://release.example/installer-test \
	"$INSTALL_SH" "$@"
}

run_bootstrap x86_64 install --dir /opt/agent-compose --yes
grep -Fxq 'install --dir /opt/agent-compose --yes' "$EXEC_LOG" || fail 'amd64 arguments were not forwarded'
grep -Fq '/installer-test/agent-compose-installer-linux-amd64' "$CURL_LOG" || fail 'amd64 asset was not downloaded'

run_bootstrap aarch64 uninstall --purge --yes
grep -Fxq 'uninstall --purge --yes' "$EXEC_LOG" || fail 'arm64 arguments were not forwarded'
grep -Fq '/installer-test/agent-compose-installer-linux-arm64' "$CURL_LOG" || fail 'arm64 asset was not downloaded'

cp "$FAKE_RELEASE/SHASUMS256.txt" "$FAKE_RELEASE/SHASUMS256.txt.canonical"
sed -i 's#  agent-compose-installer#  ./agent-compose-installer#' "$FAKE_RELEASE/SHASUMS256.txt"
run_bootstrap x86_64 install --yes
sed -i 's#  \./agent-compose-installer# *./agent-compose-installer#' "$FAKE_RELEASE/SHASUMS256.txt"
run_bootstrap aarch64 install --yes
mv "$FAKE_RELEASE/SHASUMS256.txt.canonical" "$FAKE_RELEASE/SHASUMS256.txt"

if AGENT_COMPOSE_UNAME_S=Darwin AGENT_COMPOSE_UNAME_M=arm64 "$INSTALL_SH" >/dev/null 2>&1; then
  fail 'unsupported operating system was accepted'
fi
if AGENT_COMPOSE_UNAME_S=Linux AGENT_COMPOSE_UNAME_M=riscv64 "$INSTALL_SH" >/dev/null 2>&1; then
  fail 'unsupported architecture was accepted'
fi

cp "$FAKE_RELEASE/SHASUMS256.txt" "$FAKE_RELEASE/SHASUMS256.txt.good"
sed -i 's/^[[:xdigit:]]/0/' "$FAKE_RELEASE/SHASUMS256.txt"
if run_bootstrap amd64 install --yes >/dev/null 2>&1; then
  fail 'invalid checksum was accepted'
fi
mv "$FAKE_RELEASE/SHASUMS256.txt.good" "$FAKE_RELEASE/SHASUMS256.txt"

printf 'install_bootstrap_test: all checks passed\n'
