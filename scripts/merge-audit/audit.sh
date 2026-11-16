#!/bin/bash
# audit.sh [since]: the daily merge audit, on the Mac mini at 04:30 (ai.isoform.merge-audit,
# installed by install.sh), before the 05:00 daily trial.
#
# Audits the merges into feat/iphone-companion of the last 24 hours (or since [since], any
# `git log --since` date) with audit.py, keeps the report in ~/isoform-mq/audit/<date>.md and
# appends to ~/isoform-mq/audit.log one line:
#   2026-11-16 04:30 OK 12 merges to 1a2b3c4d5: 0 regressions, 9 notes (<report>)
# and, for each silent loss, a line that starts with REGRESSION:
#   REGRESSION 2026-11-16 04:30 stale f545e4900 #1034 claude/leak-scan: wails-gui/app.go is the version before ...
# A run that could not audit writes a line with ERROR. Reads the repository only (a fetch).
set -u
export PATH=/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin
HERE="$(cd "$(dirname "$0")" && pwd)"
Q=$HOME/isoform-mq; R=${MERGE_AUDIT_REPO:-$HOME/src/isoform}; BASE=feat/iphone-companion
since=${1:-24 hours ago}
mkdir -p "$Q/audit"
stamp=$(date '+%Y-%m-%d %H:%M')
log() { printf '%s\n' "$1" >> "$Q/audit.log"; }

cd "$R" || { log "$stamp ERROR no repository at $R"; exit 2; }
fetched=""
for i in 1 2 3; do # the merge queue fetches into the same repository
  git fetch -q origin "+refs/heads/$BASE:refs/remotes/origin/$BASE" && { fetched=1; break; }
  sleep 20
done
[ -n "$fetched" ] || { log "$stamp ERROR could not fetch $BASE"; exit 2; }

report="$Q/audit/$(date +%Y-%m-%d).md"
out=$(nice -n 10 python3 -I "$HERE/audit.py" "origin/$BASE" "$since" "$report"); rc=$?
[ -n "$out" ] || out="ERROR audit.py printed nothing (exit $rc)"
first=1
while IFS= read -r line; do
  if [ -n "$first" ]; then log "$stamp $line"; first=""
  else log "${line%% *} $stamp ${line#* }"; fi
done <<< "$out"
exit $rc
