#!/bin/bash
# Rerun the failed jobs of Claude runs (claude, claude-qa, claude-ci-fix) that an API error ended, such as the usage
# limit running out: the action then logs "subtype success with is_error:true". A run is retried only when every one of
# its failed jobs shows that, since `gh run rerun --failed` reruns them all. Waits an hour after each failed attempt, so
# a still-limited account does not use up attempts, and stops after 6 attempts (about 5 hours, one limit window).
# claude runs are retried only while their issue or PR (the "#N" that leads the run name) is open. Looks back 24 hours.
# A second pass restarts claude issues whose run ended without a PR (see below). DRY_RUN=1 only prints the decisions. Needs GH_TOKEN and GITHUB_REPOSITORY (owner/name).
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

# Open issues labelled "claude" whose last claude run succeeded without leaving an open PR (the session ended early, or
# Claude stopped to ask): label them again, which starts the claude workflow, an hour after that run and at most 3 runs
# per issue. Failed runs are left to the rerun pass above.
issues=$(gh api "repos/$R/issues?state=open&labels=claude&per_page=100" --paginate --jq '.[]|select(.pull_request|not)|.number')   || { echo "cannot list claude issues"; exit 0; }
week=$(date -u -d '-7 days' +%Y-%m-%dT%H:%M:%SZ)
issue_runs=$(gh api "repos/$R/actions/workflows/claude.yml/runs?created=>$week&per_page=100" --paginate   --jq '.workflow_runs[]|select(.conclusion!="skipped")|[(.display_title|capture("^#(?<n>[0-9]+) ").n // ""), .status, (.conclusion // ""), .updated_at, .event]|@tsv')   || { echo "cannot list claude runs"; exit 0; }
# At most 3 issues per pass (twice an hour), so a backlog does not use up the Claude usage limit at once.
started=0
for n in $issues; do
  if [ "$started" -ge 3 ]; then echo "later #$n (3 issues restarted this pass)"; continue; fi
  all=$(awk -F'	' -v n="$n" '$1==n' <<<"$issue_runs")
  # An @claude comment run is active work too, but only the runs the label started count as tries.
  mine=$(awk -F'	' '$5=="issues"' <<<"$all")
  # No run this week (runs from before claude.yml had a run-name carry no number) counts as never tried.
  if [ -n "$all" ] && awk -F'	' '$2!="completed"' <<<"$all" | grep -q .; then echo "wait #$n (a claude run is active)"; continue; fi
  if [ -n "$mine" ]; then
    last=$(sort -t$'	' -k4,4r <<<"$mine" | head -1)
    IFS=$'	' read -r _ _ conclusion updated _ <<<"$last"
    if [ "$conclusion" != success ]; then echo "skip #$n (last run: $conclusion)"; continue; fi
    if [[ ! "$updated" < "$cutoff" ]]; then echo "wait #$n (last run ended under an hour ago)"; continue; fi
  fi
  if ! prs=$(gh api "repos/$R/issues/$n/timeline" --paginate --jq '.[]|select(.event=="cross-referenced" and .source.issue.pull_request!=null and .source.issue.state=="open")|.source.issue.number'); then
    echo "wait #$n (cannot read its timeline)"; continue
  fi
  if [ -n "$prs" ]; then echo "skip #$n (open PR $(tr '
' ' ' <<<"$prs"))"; continue; fi
  tries=$(grep -c . <<<"$mine")
  why=$([ "$tries" = 0 ] && echo "no claude run this week" || echo "run $tries ended without a PR")
  if [ "$tries" -ge 3 ]; then echo "give up #$n ($tries runs and no PR: needs a person)"; continue; fi
  started=$((started + 1))
  if [ "${DRY_RUN:-}" = 1 ]; then echo "would relabel #$n ($why)"
  elif gh issue edit "$n" -R "$R" --remove-label claude >/dev/null; then
    # Without the label the next pass cannot find the issue, so retry the add before giving up.
    for try in 1 2 3; do gh issue edit "$n" -R "$R" --add-label claude >/dev/null && break; try=; sleep 5; done
    if [ -n "${try:-}" ]; then echo "relabeled #$n ($why)"; else echo "relabel failed #$n: claude label is removed, add it by hand"; fi
  else echo "relabel failed #$n"; fi
done
exit 0
