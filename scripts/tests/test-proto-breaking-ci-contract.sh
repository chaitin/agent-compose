#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
PROTO_WORKFLOW="$ROOT_DIR/.github/workflows/proto-compatibility.yml"
APPROVAL_WORKFLOW="$ROOT_DIR/.github/workflows/proto-compatibility-review.yml"

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
approval_workflow=$(<"$APPROVAL_WORKFLOW")

require_workflow() {
  if ! grep -Eq -- "$1" <<<"$workflow"; then
    printf 'test-proto-breaking-ci-contract: missing %s\n' "$2" >&2
    exit 1
  fi
}

require_workflow 'types: \[opened, synchronize, reopened, ready_for_review\]' 'PR revision triggers'
require_workflow 'pull-requests:[[:space:]]*read' 'pull request read permission'
require_workflow 'cancel-in-progress:[[:space:]]*false' 'review-safe compatibility concurrency'
require 'fetch-depth:[[:space:]]*0' 'complete Git history checkout'
require 'ref: refs/pull/\$\{\{ github\.event\.pull_request\.number \}\}/merge' 'PR merge revision checkout'
require 'buf@v1\.68\.1' 'pinned Buf version'
require 'PROTO_BASE: \.git#ref=\$\{\{ github\.event\.pull_request\.base\.sha \}\}' 'exact PR base revision'
require 'buf breaking --against "\$PROTO_BASE" --error-format=github-actions' 'Buf breaking check with PR annotations'
require 'steps\.breaking\.outputs\.exit-code == '\''100'\''' 'breaking violation approval path'
require 'select\(\.state == "APPROVED" and \.commit_id == \$head\)' 'approval for current PR commit'
require 'collaborators/\$reviewer/permission' 'reviewer repository permission lookup'
require 'permissions\.maintain == true' 'maintain permission requirement'
require 'permissions\.admin == true' 'admin permission requirement'

require_approval_workflow() {
  if ! grep -Eq -- "$1" <<<"$approval_workflow"; then
    printf 'test-proto-breaking-ci-contract: approval workflow missing %s\n' "$2" >&2
    exit 1
  fi
}

if grep -Eq 'pull_request_review:|labeled|unlabeled|protobuf-breaking-approved' <<<"$workflow"; then
  echo 'test-proto-breaking-ci-contract: required workflow must not create checks for review or label events' >&2
  exit 1
fi

require_approval_workflow 'pull_request_review:' 'pull request review trigger'
require_approval_workflow 'types: \[submitted, dismissed\]' 'review submission and dismissal triggers'
require_approval_workflow "review.state == 'approved'.*review.state == 'changes_requested'" 'approval-changing review filter'
require_approval_workflow 'actions:[[:space:]]*write' 'Actions rerun permission'
require_approval_workflow 'group: proto-compatibility-\$\{\{ github\.event\.pull_request\.number \}\}' 'shared compatibility concurrency group'
require_approval_workflow 'head_sha=\$PR_HEAD_SHA' 'current PR commit run lookup'
require_approval_workflow '\.workflow_runs \| sort_by\(\.created_at\) \| last \| \.id' 'latest workflow run selection'
require_approval_workflow 'actions/runs/\$run_id/rerun' 'original compatibility run retry'

if grep -Eq 'actions/checkout|buf breaking' <<<"$approval_workflow"; then
  echo 'test-proto-breaking-ci-contract: approval workflow must not checkout or execute PR code' >&2
  exit 1
fi
