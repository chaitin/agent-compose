#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
PROTO_WORKFLOW="$ROOT_DIR/.github/workflows/proto-compatibility.yml"
APPROVAL_WORKFLOW="$ROOT_DIR/.github/workflows/proto-compatibility-review.yml"
RERUN_WORKFLOW="$ROOT_DIR/.github/workflows/proto-compatibility-rerun.yml"

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
rerun_workflow=$(<"$RERUN_WORKFLOW")

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
require_approval_workflow 'group: proto-compatibility-\$\{\{ github\.event\.pull_request\.number \}\}' 'shared compatibility concurrency group'

# Fork review events only get a read-only token, so the review workflow must not
# depend on write access; the rerun happens in the workflow_run follower.
if grep -Eq 'actions:[[:space:]]*write|/rerun' <<<"$approval_workflow"; then
  echo 'test-proto-breaking-ci-contract: approval workflow must not need write access; rerun belongs to proto-compatibility-rerun.yml' >&2
  exit 1
fi

if grep -Eq 'actions/checkout|buf breaking' <<<"$approval_workflow"; then
  echo 'test-proto-breaking-ci-contract: approval workflow must not checkout or execute PR code' >&2
  exit 1
fi

require_rerun_workflow() {
  if ! grep -Eq -- "$1" <<<"$rerun_workflow"; then
    printf 'test-proto-breaking-ci-contract: rerun workflow missing %s\n' "$2" >&2
    exit 1
  fi
}

# workflow_run names the workflow it follows, so a rename of the approval
# workflow would silently stop every rerun.
approval_name=$(sed -n 's/^name:[[:space:]]*//p' "$APPROVAL_WORKFLOW" | head -1)
if ! grep -Fq "workflows: [$approval_name]" <<<"$rerun_workflow"; then
  printf 'test-proto-breaking-ci-contract: rerun workflow must follow %s\n' "$approval_name" >&2
  exit 1
fi
require_rerun_workflow 'workflow_run:' 'workflow_run trigger'
require_rerun_workflow 'types: \[completed\]' 'completed approval runs trigger'
require_rerun_workflow "workflow_run\.event == 'pull_request_review'" 'guard against non-review runs'
require_rerun_workflow "workflow_run\.conclusion == 'success'" 'guard against skipped approval runs'
require_rerun_workflow 'actions:[[:space:]]*write' 'Actions rerun permission'
require_rerun_workflow 'PR_HEAD_SHA: \$\{\{ github\.event\.workflow_run\.head_sha \}\}' 'reviewed commit from workflow_run'
require_rerun_workflow '\^\[0-9a-f\]\{40\}\$' 'reviewed commit format validation'
require_rerun_workflow 'event=pull_request&head_sha=\$PR_HEAD_SHA' 'current PR commit run lookup'
require_rerun_workflow '\.workflow_runs \| sort_by\(\.created_at\) \| last \| \.id' 'latest workflow run selection'
require_rerun_workflow 'actions/runs/\$run_id/rerun' 'original compatibility run retry'

if grep -Eq 'actions/checkout|buf breaking|contents:[[:space:]]*write|pull-requests:[[:space:]]*write' <<<"$rerun_workflow"; then
  echo 'test-proto-breaking-ci-contract: rerun workflow must not checkout PR code or hold extra write permissions' >&2
  exit 1
fi
