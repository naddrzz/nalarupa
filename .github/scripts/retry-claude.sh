#!/bin/bash
# Rerun the failed jobs of Claude runs (claude, claude-qa, claude-ci-fix) that an API error ended, such as the usage
# limit running out: the action then logs "subtype success with is_error:true". Waits an hour after each failed attempt,
# so a still-limited account does not use up attempts, and stops after 6 attempts (about 5 hours, one limit window).
# claude runs are retried only while their issue or PR is still open. Looks back 24 hours. DRY_RUN=1 only prints the
# decisions. Needs GH_TOKEN and GITHUB_REPOSITORY (owner/name).
set -u
R=${GITHUB_REPOSITORY:?}
since=$(date -u -d '-24 hours' +%Y-%m-%dT%H:%M:%SZ) cutoff=$(date -u -d '-60 minutes' +%Y-%m-%dT%H:%M:%SZ)
# The issues endpoint lists PRs too; a claude run's title is its issue's or PR's title.
open=$(gh api "repos/$R/issues?state=open&per_page=100" --paginate --jq '.[].title') || { echo "cannot list open issues"; exit 1; }
runs=$(gh api "repos/$R/actions/runs?status=failure&created=>$since&per_page=100" --paginate --jq '.workflow_runs[]
  | select(.name=="claude" or .name=="claude-qa" or .name=="claude-ci-fix")
  | [.id, .name, .run_attempt, .updated_at, .display_title] | @tsv') || { echo "cannot list failed runs"; exit 1; }
while IFS=$'\t' read -r id wf attempt updated title; do
  [ -n "$id" ] || continue
  if [[ ! "$updated" < "$cutoff" ]]; then echo "wait $id ($wf, failed under an hour ago) $title"
  elif [ "$attempt" -ge 6 ]; then echo "give up $id ($wf, $attempt attempts) $title"
  elif [ "$wf" = claude ] && ! grep -qxF -- "$title" <<<"$open"; then echo "skip $id ($wf, issue or PR closed) $title"
  elif ! gh run view "$id" -R "$R" --log-failed 2>/dev/null | grep -q 'subtype success with is_error:true'; then
    echo "skip $id ($wf, failed for another reason) $title"
  elif [ "${DRY_RUN:-}" = 1 ]; then echo "would rerun $id ($wf, attempt $attempt) $title"
  elif gh run rerun "$id" -R "$R" --failed; then echo "reran $id ($wf, attempt $attempt) $title"
  else echo "rerun failed $id ($wf) $title"; fi
done <<<"$runs"
exit 0
