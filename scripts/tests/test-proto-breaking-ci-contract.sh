#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
PROTO_WORKFLOW="$ROOT_DIR/.github/workflows/proto-compatibility.yml"

job=$(awk '
  $0 == "  proto-breaking:" {
    found = 1
  }
  found && $0 ~ /^  [[:alnum:]_-]+:[[:space:]]*$/ && $0 != "  proto-breaking:" {
    exit
  }
  found {
    print
  }
  END {
    if (!found) {
      exit 1
    }
  }
' "$PROTO_WORKFLOW")

require() {
  if ! grep -Eq -- "$1" <<<"$job"; then
    printf 'test-proto-breaking-ci-contract: missing %s\n' "$2" >&2
    exit 1
  fi
}

workflow=$(<"$PROTO_WORKFLOW")

require_workflow() {
  if ! grep -Eq -- "$1" <<<"$workflow"; then
    printf 'test-proto-breaking-ci-contract: missing %s\n' "$2" >&2
    exit 1
  fi
}

require_workflow 'pull_request_review:' 'pull request review trigger'
require_workflow 'types: \[submitted, dismissed\]' 'review submission and dismissal triggers'
require_workflow 'types: \[opened, synchronize, reopened, labeled, unlabeled\]' 'PR and label triggers'
require_workflow 'pull-requests:[[:space:]]*read' 'pull request read permission'
require 'fetch-depth:[[:space:]]*0' 'complete Git history checkout'
require 'ref: refs/pull/\$\{\{ github\.event\.pull_request\.number \}\}/merge' 'PR merge revision checkout'
require 'buf@v1\.68\.1' 'pinned Buf version'
require 'PROTO_BASE: \.git#ref=\$\{\{ github\.event\.pull_request\.base\.sha \}\}' 'exact PR base revision'
require 'buf breaking --against "\$PROTO_BASE" --error-format=github-actions' 'Buf breaking check with PR annotations'
require 'steps\.breaking\.outputs\.exit-code == '\''100'\''' 'breaking violation approval path'
require 'REQUIRED_LABEL: protobuf-breaking-approved' 'breaking approval label'
require 'select\(\.state == "APPROVED" and \.commit_id == \$head\)' 'approval for current PR commit'
require 'collaborators/\$reviewer/permission' 'reviewer repository permission lookup'
require 'permissions\.maintain == true' 'maintain permission requirement'
require 'permissions\.admin == true' 'admin permission requirement'
