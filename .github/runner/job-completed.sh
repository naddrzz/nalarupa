#!/bin/bash
# Runs after every job on the VPS runners (ACTIONS_RUNNER_HOOK_JOB_COMPLETED in each runner's .env) so QA, CI and
# Claude jobs don't fill the shared disk. Installed as ~/bin/job-completed.sh on the runner user; copy it there
# again after editing. Never fails the job and only touches the runner user's own files.
set +e
me=$(id -un)
kb() { { du -sk "$1" 2>/dev/null || echo 0; } | awk '{print $1}'; }
free_gb() { df -k / | awk 'NR==2{print int($4/1048576)}'; }
before=$(free_gb)

# This job's leftovers in the workspace (checkout re-cleans tracked files anyway).
[ -n "$GITHUB_WORKSPACE" ] && rm -rf "$GITHUB_WORKSPACE/app/screenshots" "$GITHUB_WORKSPACE/app/.qa"

# Temp files older than the longest job (150 min), so a job running on the other runner keeps its own.
find /tmp -mindepth 1 -user "$me" -mmin +180 \( -type f -o -type l \) -delete 2>/dev/null
find /tmp -mindepth 1 -user "$me" -mmin +180 -type d -empty -delete 2>/dev/null

# This runner's Rust target dir (NALARUPA_TARGET, set per runner): incremental builds pile up, so start over past
# the cap, or as soon as the disk runs low (other projects share this disk).
cap=${NALARUPA_TARGET_CAP_GB:-4}
[ "$before" -lt 15 ] && cap=0
[ -n "$NALARUPA_TARGET" ] && [ "$(kb "$NALARUPA_TARGET")" -gt $((cap * 1048576)) ] && rm -rf "$NALARUPA_TARGET"

# Package cache and old Claude Code versions (keep the two newest). Both are shared by the runners, so only touch
# them when this job's worker is the only one running (this hook runs before its worker exits).
if [ "$(pgrep -u "$me" -fc 'Runner\.Worker spawnclient')" -le 1 ]; then
  [ "$(kb "$HOME/.bun/install/cache")" -ge 2097152 ] && rm -rf "$HOME/.bun/install/cache"
  ls -1t "$HOME/.local/share/claude/versions" 2>/dev/null | tail -n +3 | sed "s#^#$HOME/.local/share/claude/versions/#" | xargs -r rm -rf
fi

echo "cleanup: disk free ${before}G -> $(free_gb)G"
exit 0
