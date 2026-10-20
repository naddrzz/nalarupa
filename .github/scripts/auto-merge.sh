#!/bin/bash
# Merge every open PR into dev that passes the gate: no check still running, `checks` and `windows` (ci) passed, cubic's review
# finished, CodeRabbit not pending, every thread started by cubic or CodeRabbit resolved, and GitHub says MERGEABLE.
# A PR that conflicts with dev gets an @claude request to merge dev in (claude workflow). Skips drafts, forks and PRs
# labelled no-auto-merge. DRY_RUN=1 only prints the decisions. Needs GH_TOKEN and
# GITHUB_REPOSITORY (owner/name).
set -u
R=${GITHUB_REPOSITORY:?}
owner=${R%%/*} name=${R#*/}
prs=$(gh pr list -R "$R" --state open --base dev --limit 1000 --json number,isDraft,isCrossRepository,labels \
        --jq '.[] | select((.isDraft|not) and (.isCrossRepository|not) and ([.labels[].name]|index("no-auto-merge")|not)) | .number') \
  || { echo "gh pr list failed"; exit 1; }
me=$(gh api user --jq .login) || me=
# ask KIND TEXT: post an @claude request on PR $n as the owner, so the claude workflow handles it. Once per head commit
# and at most 3 times per PR for each KIND; only markers from our own account count (anyone can comment), and a failed
# lookup waits instead of asking again.
ask() {
  local kind=$1 text=$2 head comments marks
  if ! head=$(gh pr view "$n" -R "$R" --json headRefOid --jq .headRefOid) || [ -z "$head" ] || [ -z "$me" ] \
     || ! comments=$(gh api "repos/$R/issues/$n/comments" --paginate --jq ".[]|select(.user.login==\"$me\")|.body"); then
    echo "wait #$n ($kind, cannot read head or comment history) $title"; return
  fi
  marks=$(grep -o "<!-- auto-merge:$kind [0-9a-f]* -->" <<<"$comments" || :)
  if grep -qF "$head" <<<"$marks"; then echo "wait #$n ($kind, claude already asked for ${head:0:7}) $title"
  elif [ "$(grep -c . <<<"$marks")" -ge 3 ]; then echo "wait #$n ($kind, asked 3 times: needs a person) $title"
  elif [ "${DRY_RUN:-}" = 1 ]; then echo "would ask claude ($kind) in #$n $title"
  else
    gh pr comment "$n" -R "$R" --body "@claude $text
<!-- auto-merge:$kind $head -->" >/dev/null && echo "asked claude ($kind) in #$n $title"
  fi
}
for n in $prs; do
  info=$(gh pr view "$n" -R "$R" --json mergeable,statusCheckRollup,title --jq '
    [.mergeable,
     ([.statusCheckRollup[]|select((.__typename=="CheckRun" and .status!="COMPLETED" and .name!="auto-merge") or (.__typename=="StatusContext" and (.state=="PENDING" or .state=="EXPECTED")))]|length),
     ([.statusCheckRollup[]|select(.name=="checks")|.conclusion]|last // "NONE"),
     ([.statusCheckRollup[]|select(.name=="windows")|.conclusion]|last // "NONE"),
     ([.statusCheckRollup[]|select((.name // "")|startswith("cubic"))|.status]|last // "NONE"),
     ([.statusCheckRollup[]|select(.context=="CodeRabbit")|.state]|last // "NONE"),
     .title] | map(tostring) | join("|")')
  IFS="|" read -r mergeable running ci win cubic rabbit title <<<"$info"
  # --paginate runs the query once per page of 100 threads; --jq prints one line per open bot thread, wc counts them.
  # A failed query leaves $open empty, which the gate treats as "wait".
  open=$(gh api graphql --paginate -f query="query(\$endCursor:String){repository(owner:\"$owner\",name:\"$name\"){pullRequest(number:$n){reviewThreads(first:100,after:\$endCursor){pageInfo{hasNextPage endCursor} nodes{isResolved comments(first:1){nodes{author{login}}}}}}}}" \
           --jq '.data.repository.pullRequest.reviewThreads.nodes[]|select((.comments.nodes[0].author.login=="cubic-dev-ai" or .comments.nodes[0].author.login=="coderabbitai") and (.isResolved|not))|"open"' | wc -l; [ "${PIPESTATUS[0]}" = 0 ]) || open=
  state="mergeable=$mergeable running=$running ci=$ci windows=$win cubic=$cubic coderabbit=$rabbit open_bot_threads=$open"
  if [ "$mergeable" = MERGEABLE ] && [ "$running" = 0 ] && [ "$ci" = SUCCESS ] && [ "$win" = SUCCESS ] && [ "$cubic" = COMPLETED ] && [ "$rabbit" != PENDING ] && [ "$open" = 0 ]; then
    if [ "${DRY_RUN:-}" = 1 ]; then echo "would merge #$n ($state) $title"
    elif gh pr merge "$n" -R "$R" --merge --subject "Merge #$n: $title"; then echo "merged #$n $title"
    else echo "merge failed #$n ($state)"; fi
  elif [ "$mergeable" = CONFLICTING ]; then
    # Nothing else resolves conflicts (parallel fix PRs often touch the same files).
    ask conflict "This PR conflicts with \`dev\`. Merge \`origin/dev\` into this branch and resolve every conflict so both sides keep working: the fixes already merged into dev stay, and this PR's change still does what it set out to do. Run the checks from the claude workflow, then push to this branch. Do not open a new PR."
  elif [ "$running" = 0 ] && [ "$ci" != FAILURE ] && [ "$win" = SUCCESS ] && [ "$cubic" = COMPLETED ] && [ "$rabbit" != PENDING ] && [ -n "$open" ] && [ "$open" -gt 0 ]; then
    # Review threads still open with nothing queued to handle them: review-fix ended without resolving them (or never
    # ran), and no new review will start it again. A failed ci run is left to claude-ci-fix: both would push to the branch.
    ask threads "$open review thread(s) from cubic or CodeRabbit are still unresolved on this PR and nothing is queued to handle them. For each one: fix a real problem at the root cause, or reply on the thread explaining why the finding is wrong. Run the checks from the claude workflow, then push to this branch. Only after the push, resolve each thread (gh api graphql -f query='mutation{resolveReviewThread(input:{threadId:\"<thread id>\"}){thread{isResolved}}}'): with no open thread left the PR can merge, so resolving first could merge the old head. Do not open a new PR."
  else
    echo "wait #$n ($state) $title"
  fi
done
exit 0
