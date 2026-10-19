#!/bin/bash
# Rerun the failed jobs of Claude runs (claude, claude-qa, claude-ci-fix) that an API error ended, such as the usage
# limit running out: the action then logs "subtype success with is_error:true". A run is retried only when every one of
# its failed jobs shows that, since `gh run rerun --failed` reruns them all. Waits an hour after each failed attempt, so
# a still-limited account does not use up attempts, and stops after 6 attempts (about 5 hours, one limit window).
# claude runs are retried only while their issue or PR (the "#N" that leads the run name) is open. Looks back 24 hours.
# DRY_RUN=1 only prints the decisions. Needs GH_TOKEN and GITHUB_REPOSITORY (owner/name).
set -u
R=${GITHUB_REPOSITORY:?}
since=$(date -u -d '-24 hours' +%Y-%m-%dT%H:%M:%SZ) cutoff=$(date -u -d '-60 minutes' +%Y-%m-%dT%H:%M:%SZ)
# Select by workflow file: a run's `name` is its run-name (claude runs are "#N <title>").
runs=$(gh api "repos/$R/actions/runs?status=failure&created=>$since&per_page=100" --paginate --jq '.workflow_runs[]
  | (.path | ltrimstr(".github/workflows/") | rtrimstr(".yml")) as $wf
  | select($wf=="claude" or $wf=="claude-qa" or $wf=="claude-ci-fix")
  | [.id, $wf, .run_attempt, .updated_at, .display_title] | @tsv') || { echo "cannot list failed runs"; exit 1; }
# why ID: prints why run ID should not be retried; prints nothing when all its failed jobs ended on an API error.
why() {
  local jobs j log
  jobs=$(gh api "repos/$R/actions/runs/$1/jobs?per_page=100" --paginate --jq '.jobs[]|select(.conclusion=="failure")|.id') \
    || { echo "cannot list its jobs"; return; }
  [ -n "$jobs" ] || { echo "no failed job"; return; }
  for j in $jobs; do
    log=$(gh api "repos/$R/actions/jobs/$j/logs") || { echo "cannot read the log of job $j"; return; }
    grep -q 'subtype success with is_error:true' <<<"$log" || { echo "job $j failed for another reason"; return; }
  done
}
while IFS=$'\t' read -r id wf attempt updated title; do
  [ -n "$id" ] || continue
  if [[ ! "$updated" < "$cutoff" ]]; then echo "wait $id ($wf, failed under an hour ago) $title"; continue; fi
  if [ "$attempt" -ge 6 ]; then echo "give up $id ($wf, $attempt attempts) $title"; continue; fi
  if [ "$wf" = claude ]; then
    num=$(sed -n 's/^#\([0-9][0-9]*\) .*/\1/p' <<<"$title")
    if [ -z "$num" ]; then echo "skip $id ($wf, no issue or PR number in the run name) $title"; continue; fi
    if ! state=$(gh api "repos/$R/issues/$num" --jq .state); then echo "wait $id ($wf, cannot read #$num) $title"; continue; fi
    if [ "$state" != open ]; then echo "skip $id ($wf, #$num is $state) $title"; continue; fi
  fi
  reason=$(why "$id")
  if [ -n "$reason" ]; then echo "skip $id ($wf, $reason) $title"
  elif [ "${DRY_RUN:-}" = 1 ]; then echo "would rerun $id ($wf, attempt $attempt) $title"
  elif gh run rerun "$id" -R "$R" --failed; then echo "reran $id ($wf, attempt $attempt) $title"
  else echo "rerun failed $id ($wf) $title"; fi
done <<<"$runs"
exit 0
