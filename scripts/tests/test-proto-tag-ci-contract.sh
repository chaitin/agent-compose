#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
NEXT_TAG="$ROOT_DIR/scripts/proto-next-tag.sh"
TAG_WORKFLOW="$ROOT_DIR/.github/workflows/proto-tag.yml"
CI_WORKFLOW="$ROOT_DIR/.github/workflows/ci.yml"
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT

failures=0

fail() {
  printf 'test-proto-tag-ci-contract: %s\n' "$*" >&2
  failures=$((failures + 1))
}

# --- proto-next-tag.sh against a scratch repository -------------------------

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com

repo="$TEST_ROOT/repo"
git init -q -b main "$repo"

commit_file() { # $1=path $2=content; prints the new commit
  mkdir -p "$(dirname -- "$repo/$1")"
  printf '%s\n' "$2" >"$repo/$1"
  git -C "$repo" add -- "$1"
  git -C "$repo" commit -q -m "change $1"
  git -C "$repo" rev-parse HEAD
}

expect() { # $1=description $2=commit $3=expected-status $4=expected-stdout
  local out status
  set +e
  out=$(cd "$repo" && "$NEXT_TAG" "$2" 2>/dev/null)
  status=$?
  set -e
  if [[ $status -ne $3 || $out != "$4" ]]; then
    fail "$1: got status $status output '$out', expected status $3 output '$4'"
  fi
}

base=$(commit_file proto/api.proto 'v1')
expect 'no proto tag at all' "$base" 1 ''

git -C "$repo" tag proto/v0.1.2 "$base"
git -C "$repo" tag proto/v0.1.9 "$base"
expect 'tagged commit' "$base" 0 ''

docs=$(commit_file README.md 'docs')
expect 'change outside proto/' "$docs" 0 ''

field=$(commit_file proto/api.proto 'v2')
expect 'proto change after the newest tag' "$field" 0 'proto/v0.1.10'

git -C "$repo" tag proto/v0.1.10 "$field"
rpc=$(commit_file proto/api.proto 'v3')
expect 'numeric rather than lexical version order' "$rpc" 0 'proto/v0.1.11'

git -C "$repo" tag proto/v0.1.11 "$rpc"
git -C "$repo" tag proto/v0.2.0-rc.1 "$rpc"
nested=$(commit_file proto/health/v1/health.proto 'v1')
expect 'pre-release tags ignored' "$nested" 0 'proto/v0.1.12'
expect 'late run for a commit the newest tag contains' "$field" 0 ''

git -C "$repo" checkout -q -b side "$base"
side=$(commit_file proto/api.proto 'side')
expect 'commit that does not descend from the newest tag' "$side" 1 ''
expect 'unknown commit' 'no-such-commit' 1 ''

# --- workflow wiring ---------------------------------------------------------

tag_workflow=$(<"$TAG_WORKFLOW")
ci_workflow=$(<"$CI_WORKFLOW")

require() { # $1=text $2=extended-regex $3=description
  if ! grep -Eq -- "$2" <<<"$1"; then
    fail "missing $3"
  fi
}

# workflow_run names the workflow it follows, so renaming CI would silently
# stop every proto release.
require "$ci_workflow" '^name: CI$' 'CI workflow name followed by the proto tag workflow'
require "$tag_workflow" 'workflows: \[CI\]' 'trigger on CI runs'
require "$tag_workflow" 'types: \[completed\]' 'trigger on completed CI runs'
require "$tag_workflow" 'branches: \[main\]' 'main-only trigger'
require "$tag_workflow" "workflow_run\.event == 'push'" 'guard against pull requests from a branch named main'
require "$tag_workflow" "workflow_run\.conclusion == 'success'" 'guard against failed CI runs'
require "$tag_workflow" "github\.ref == 'refs/heads/main'" 'main-only manual dispatch'
require "$tag_workflow" 'cancel-in-progress: false' 'serialized tagging'
require "$tag_workflow" 'contents: write' 'tag push permission'

# Write access belongs to the tagging job alone, not the whole workflow.
top_permissions=$(awk '
  /^permissions:/ { found = 1; next }
  found && /^[^[:space:]]/ { exit }
  found { print }
' "$TAG_WORKFLOW")
require "$top_permissions" '^  contents: read$' 'read-only workflow permissions'
if grep -q 'write' <<<"$top_permissions"; then
  fail 'forbidden write access in workflow-level permissions'
fi
require "$tag_workflow" 'fetch-depth: 0' 'checkout with every tag'
require "$tag_workflow" 'scripts/proto-next-tag\.sh "\$COMMIT"' 'tag decision for the tested commit'
require "$tag_workflow" 'git push origin "refs/tags/\$TAG"' 'tag push'
require "$tag_workflow" 'GOPROXY: https://proxy\.golang\.org$' 'resolution through the public proxy alone'

proto_version=$(awk '
  $0 == "  proto-version:" { found = 1; print; next }
  found && $0 ~ /^  [[:alnum:]_-]+:[[:space:]]*$/ { exit }
  found { print }
' "$CI_WORKFLOW")
require "$proto_version" "if: github\.ref_type == 'tag'" 'release-tag-only published proto check'
require "$proto_version" 'go mod edit -dropreplace=github\.com/chaitin/agent-compose/proto' 'build without the proto replace'

if ((failures > 0)); then
  printf 'test-proto-tag-ci-contract: %d check(s) failed\n' "$failures" >&2
  exit 1
fi

printf 'test-proto-tag-ci-contract: all checks passed\n'
