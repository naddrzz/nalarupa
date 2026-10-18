#!/bin/bash
# Merge every open PR into dev that passes the gate: no check still running, `checks` (ci) passed, cubic's review
# finished, CodeRabbit not pending, every thread started by cubic or CodeRabbit resolved, and GitHub says MERGEABLE.
# Skips drafts, forks and PRs labelled no-auto-merge. DRY_RUN=1 only prints the decisions. Needs GH_TOKEN and
# GITHUB_REPOSITORY (owner/name).
set -u
R=${GITHUB_REPOSITORY:?}
owner=${R%%/*} name=${R#*/}
for n in $(gh pr list -R "$R" --state open --base dev --json number,isDraft,isCrossRepository,labels \
             --jq '.[] | select((.isDraft|not) and (.isCrossRepository|not) and ([.labels[].name]|index("no-auto-merge")|not)) | .number'); do
  info=$(gh pr view "$n" -R "$R" --json mergeable,statusCheckRollup,title --jq '
    [.mergeable,
     ([.statusCheckRollup[]|select(.__typename=="CheckRun" and .status!="COMPLETED" and .name!="auto-merge")]|length),
     ([.statusCheckRollup[]|select(.name=="checks")|.conclusion]|last // "NONE"),
     ([.statusCheckRollup[]|select((.name // "")|startswith("cubic"))|.status]|last // "NONE"),
     ([.statusCheckRollup[]|select(.context=="CodeRabbit")|.state]|last // "NONE"),
     .title] | map(tostring) | join("|")')
  IFS="|" read -r mergeable running ci cubic rabbit title <<<"$info"
  open=$(gh api graphql -f query="{repository(owner:\"$owner\",name:\"$name\"){pullRequest(number:$n){reviewThreads(first:100){nodes{isResolved comments(first:1){nodes{author{login}}}}}}}}" \
           --jq '[.data.repository.pullRequest.reviewThreads.nodes[]|select((.comments.nodes[0].author.login=="cubic-dev-ai" or .comments.nodes[0].author.login=="coderabbitai") and (.isResolved|not))]|length')
  state="mergeable=$mergeable running=$running ci=$ci cubic=$cubic coderabbit=$rabbit open_bot_threads=$open"
  if [ "$mergeable" = MERGEABLE ] && [ "$running" = 0 ] && [ "$ci" = SUCCESS ] && [ "$cubic" = COMPLETED ] && [ "$rabbit" != PENDING ] && [ "$open" = 0 ]; then
    if [ "${DRY_RUN:-}" = 1 ]; then echo "would merge #$n ($state) $title"
    elif gh pr merge "$n" -R "$R" --merge --subject "Merge #$n: $title"; then echo "merged #$n $title"
    else echo "merge failed #$n ($state)"; fi
  else
    echo "wait #$n ($state) $title"
  fi
done
exit 0
