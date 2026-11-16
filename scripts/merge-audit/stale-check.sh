#!/bin/bash
# stale-check.sh <base> <head> [depth]: does <head> carry an older copy of a file?
#
# For every file <head> changes against <base>, it compares <head>'s version
# with the versions that file had on <base>'s first-parent line over the last
# [depth] commits (default 400). A match means the branch wrote back a copy
# from before a change that is already on <base>, as 7c2df4fe6 (#1034) did
# when it was squashed with `git reset --soft` onto a moved ref and undid
# #1021 and #1026.
#
# Prints one "<path> <version-before-commit> <subject>" line per hit and exits 1;
# exits 0 with no output when there is none.
#
# A PR that means to put an older version back (a restore, a revert) names
# each such file in a commit message between <base> and <head>:
#   Restores-older-copy: path/to/file
# and that file is not reported.
#
# Run from inside the repository. <base> is usually `git merge-base <tip> <head>`.
set -eu
base=${1:?base}; head=${2:?head}; depth=${3:-400}

allowed=$(git log --format=%B "$base..$head" | sed -n 's/^Restores-older-copy:[[:space:]]*//p' | sed 's/[[:space:]]*$//' | sort -u)

# Files <head> changes, with <head>'s blob; deleted files are left out (a
# deletion is never an older copy).
changes=$(git diff --raw --no-abbrev --no-renames "$base" "$head" | awk '$5 != "D" {split($0, a, "\t"); split(a[1], m, " "); print m[4] "\t" a[2]}')
[ -z "$changes" ] && exit 0

# Each first-parent commit before <base>: the version each file had before it.
# A merge is compared with its first parent only (-m --first-parent).
history=$(git log --first-parent -m --raw --no-abbrev --no-renames --format='C %H' -n "$depth" "$base")

hits=$(awk -F'\t' '
  NR == FNR { want[$2] = $1; next }
  /^C / { commit = substr($0, 3); next }
  /^:/ {
    split($1, m, " "); old = m[3]; path = $2
    if ((path in want) && !(path in seen) && old == want[path] && old !~ /^0+$/) {
      seen[path] = 1; print path "\t" commit
    }
  }' <(printf '%s\n' "$changes") <(printf '%s\n' "$history"))

out=""
while IFS=$'\t' read -r path commit; do
  [ -z "$path" ] && continue
  if [ -n "$allowed" ] && grep -qxF -- "$path" <<< "$allowed"; then continue; fi
  out+="$path ${commit:0:9} $(git log -1 --format=%s "$commit" | cut -c1-90)"$'\n'
done <<< "$hits"
[ -z "$out" ] && exit 0
printf '%s' "$out"
exit 1
