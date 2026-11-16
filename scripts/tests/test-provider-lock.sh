#!/usr/bin/env bash
#
# Contract checks for guest-images/providers.lock.json: it must stay the single
# authoring point for provider versions, its generated ABI manifest must match,
# and the check itself must actually detect drift.
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
PROVIDER_LOCK="$ROOT_DIR/scripts/provider-lock.sh"
LOCK_FILE="$ROOT_DIR/guest-images/providers.lock.json"
ABI_FILE="$ROOT_DIR/guest-images/guest-abi.json"

failures=0
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

fail() {
  printf 'test-provider-lock: %s\n' "$*" >&2
  failures=$((failures + 1))
}

# 1. The committed tree must already be consistent.
if ! "$PROVIDER_LOCK" check >/dev/null 2>&1; then
  fail 'provider-lock check failed on the committed tree'
fi

# 2. The generated manifest must describe every agent provider with a version
#    and at least one capability, in the provider vocabulary the daemon knows.
manifest_providers=$(jq -r '.providers | keys_unsorted | join(",")' "$ABI_FILE")
if [[ $manifest_providers != 'codex,claude,opencode,pi,dsh' ]]; then
  fail "guest ABI manifest providers are '$manifest_providers', want 'codex,claude,opencode,pi,dsh'"
fi

if ! jq -e '.guestAbi | type == "number" and . >= 1' "$ABI_FILE" >/dev/null; then
  fail 'guest ABI manifest guestAbi is not a positive number'
fi

for kind in codex claude opencode pi dsh; do
  if ! jq -e --arg kind "$kind" '.providers[$kind] | (.version | type == "string" and length > 0)' "$ABI_FILE" >/dev/null; then
    fail "guest ABI manifest has no version for provider $kind"
  fi
  feature_count=$(jq -r --arg kind "$kind" '.providers[$kind].features | length' "$ABI_FILE")
  if [[ ${feature_count:-0} -lt 1 ]]; then
    fail "guest ABI manifest declares no feature for provider $kind"
  fi
done

unknown_features=$(jq -r '
  .providers
  | to_entries
  | map(.value.features[])
  | unique
  | map(select(. != "structured_output" and . != "session_resume" and . != "streaming" and . != "skill_injection"))
  | join(",")
' "$ABI_FILE")
if [[ -n $unknown_features ]]; then
  fail "guest ABI manifest uses unknown capability keys: $unknown_features"
fi

# 3. The point of the split: a provider-only bump must not move the guest ABI.
#    Otherwise every provider upgrade would again require a daemon release.
bumped_lock="$TEST_ROOT/providers.lock.json"
jq '.packages.codex.version = "99.99.99"' "$LOCK_FILE" >"$bumped_lock"

bumped_abi=$(PROVIDER_LOCK_FILE="$bumped_lock" "$PROVIDER_LOCK" guest-abi)
if [[ $(jq -r '.guestAbi' <<<"$bumped_abi") != "$(jq -r '.guestAbi' "$ABI_FILE")" ]]; then
  fail 'a provider-only bump changed the guest ABI'
fi
if [[ $(jq -r '.providers.codex.version' <<<"$bumped_abi") != '99.99.99' ]]; then
  fail 'a provider bump did not reach the guest ABI manifest'
fi

# 4. Drift must fail the check. A check that cannot fail does not protect
#    anything, so each drift direction gets a regression case.
if PROVIDER_LOCK_FILE="$bumped_lock" "$PROVIDER_LOCK" check >"$TEST_ROOT/bumped.err" 2>&1; then
  fail 'provider-lock check accepted a lock that disagrees with the Dockerfiles'
fi

stale_abi="$TEST_ROOT/guest-abi.json"
jq '.providers.codex.version = "0.0.1"' "$ABI_FILE" >"$stale_abi"
if GUEST_ABI_FILE="$stale_abi" "$PROVIDER_LOCK" check >"$TEST_ROOT/stale.err" 2>&1; then
  fail 'provider-lock check accepted a stale guest ABI manifest'
fi

# 5. sync must be idempotent, so regenerating after a bump is a no-op diff.
synced_abi="$TEST_ROOT/synced.json"
GUEST_ABI_FILE="$synced_abi" "$PROVIDER_LOCK" sync >/dev/null
if ! diff -q "$ABI_FILE" "$synced_abi" >/dev/null; then
  fail 'provider-lock sync is not idempotent with the committed manifest'
fi

# 6. A recorded retirement must fire exactly when the superseding release is
#    pinned. "We will drop the adapter when we upgrade pi" has to be enforced by
#    the upgrade itself, otherwise the stale companion is carried forever. The
#    fixture injects a companion because the real lock no longer carries one.
retired_lock="$TEST_ROOT/providers-retired.lock.json"
jq '.packages["pi-mcp-adapter"] = {
      "npm": "pi-mcp-adapter",
      "version": "2.11.0",
      "buildArg": "PI_MCP_ADAPTER_VERSION",
      "companionOf": "pi",
      "retiredBy": {
        "package": "pi",
        "version": "0.99.0",
        "reason": "test fixture records a released retirement"
      }
    }
    | .packages.pi.version = "1.0.0"' "$LOCK_FILE" >"$retired_lock"
if PROVIDER_LOCK_FILE="$retired_lock" "$PROVIDER_LOCK" check >"$TEST_ROOT/retired.err" 2>&1; then
  fail 'provider-lock check accepted pi-mcp-adapter next to a pi release that supersedes it'
elif ! grep -q 'pi-mcp-adapter' "$TEST_ROOT/retired.err"; then
  fail 'provider-lock check failed but did not name the retired package'
fi

#    Removing the package must clear the gate, so the upgrade has a way through.
#    The check still fails on the Dockerfile drift a temp lock always has; the
#    assertion is that the retirement itself is no longer reported.
retired_fixed_lock="$TEST_ROOT/providers-retired-fixed.lock.json"
jq 'del(.packages["pi-mcp-adapter"])' "$retired_lock" >"$retired_fixed_lock"
PROVIDER_LOCK_FILE="$retired_fixed_lock" "$PROVIDER_LOCK" check >"$TEST_ROOT/retired-fixed.err" 2>&1 || true
if grep -q 'retired by' "$TEST_ROOT/retired-fixed.err"; then
  fail 'provider-lock still reported a retirement after the package was removed from the lock'
fi

# A prerelease of the superseding version must not trigger the retirement gate.
retired_prerelease_lock="$TEST_ROOT/providers-retired-prerelease.lock.json"
jq '.packages.pi.version = "0.99.0-rc.1"' "$retired_lock" >"$retired_prerelease_lock"
PROVIDER_LOCK_FILE="$retired_prerelease_lock" "$PROVIDER_LOCK" check >"$TEST_ROOT/retired-prerelease.err" 2>&1 || true
if grep -q 'retired by' "$TEST_ROOT/retired-prerelease.err"; then
  fail 'provider-lock reported a retirement for a prerelease of the superseding version'
fi

if ((failures > 0)); then
  printf 'test-provider-lock: %d contract check(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'test-provider-lock: all checks passed\n'
