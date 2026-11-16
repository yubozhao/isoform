#!/bin/bash
# test.sh: stale-check.sh and audit.py on a throwaway repository that repeats #1034: a branch
# squashed with `git reset --soft` onto a moved feat carries an older copy of a file.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
cd "$tmp"; git init -q -b feat .; git config user.email t@example.com; git config user.name t
fail() { echo "FAIL: $*"; exit 1; }
merge() { git merge -q --no-ff -m "Merge branch 'origin/$1' into feat" "$1"; }

echo one > a.txt; echo keep > b.txt; git add .; git commit -qm base
old=$(git rev-parse HEAD)
git switch -q -c fix; echo two > a.txt; git commit -qam "a: two"; git switch -q feat; merge fix   # feat moves
git switch -q -c work "$old"; echo new > c.txt; git add c.txt; git commit -qm "c"
git reset -q --soft feat; git commit -qm "c, squashed onto the moved feat"                         # a.txt is back to one

base=$(git merge-base feat work)
out=$(bash "$HERE/stale-check.sh" "$base" work) && fail "no hit for the older copy"
grep -q '^a.txt ' <<< "$out" || fail "hit does not name a.txt: $out"
grep -q '^c.txt ' <<< "$out" && fail "a new file reported: $out"

git switch -q -c work2 feat; echo new > c.txt; git add c.txt; git commit -qm "c, made on the tip"
bash "$HERE/stale-check.sh" "$(git merge-base feat work2)" work2 >/dev/null || fail "hit on a clean branch"

git switch -q work; git commit -q --allow-empty -m "restore on purpose" -m "Restores-older-copy: a.txt"
bash "$HERE/stale-check.sh" "$base" work >/dev/null || fail "Restores-older-copy: a.txt did not exempt it"

# The audit names the stale merge as a REGRESSION and a clean one not at all.
git switch -q feat; git branch -q -f stale work~1; merge stale; merge work2
out=$(python3 -I "$HERE/audit.py" feat "1 year ago" "$tmp/report.md") && fail "audit exit 0 with a stale merge"
grep -q '^FOUND ' <<< "$out" || fail "summary: $out"
[ "$(grep -c '^REGRESSION stale .*a.txt is the version before' <<< "$out")" = 1 ] || fail "regressions: $out"
grep -q "work2" <<< "$(grep '^REGRESSION' <<< "$out")" && fail "clean merge reported: $out"
echo "ok"
