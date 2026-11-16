# Merge audit

Finds merges into `feat/iphone-companion` that silently undid or dropped a change
feat already had. Built after 7c2df4fe6 (#1034): a branch squashed with
`git reset --soft` onto a moved ref carried older copies of five files and
undid #1021 and #1026 (restored by #1038). The first full audit is
`docs/hub-reviews/merge-audit-2026-11-15.md`.

| File | What it does |
|---|---|
| `stale-check.sh <base> <head>` | Exits 1 and names each file `<head>` changes whose version equals one that file had on `<base>`'s first-parent line in the last 400 commits (about ten days). |
| `audit.py <ref> <since> <report.md>` | The full audit of the merges since `<since>`. |
| `audit.sh [since]` | The daily job: fetch, audit the last 24 hours, append to `~/isoform-mq/audit.log`. |
| `install.sh` | On the Mac mini: copies the three to `~/bin/merge-audit/` and installs the launchd job `ai.isoform.merge-audit`, daily at 04:30 (before the 05:00 daily trial). |
| `test.sh` | Builds a throwaway repository that repeats #1034 and checks both. |

## What counts as a regression

`audit.py` writes a line starting with `REGRESSION` for:

- **merge**: a merge commit whose tree is not the one `git merge-tree --write-tree P1 P2` makes, although the merge has no conflict (something was edited by hand in the merge commit);
- **stale**: a PR head with an older copy of a file. A later PR that puts back exactly what a stale merge overwrote is a restore and goes to the report instead.

The report (`~/isoform-mq/audit/<date>.md`) also lists, for a person to read: conflicts
resolved by hand, feat-into-branch merges inside a PR branch whose resolution changed
a file with no conflict or left out lines one side added, and lines added in the window
that are gone at the tip with the merge that removed them. Most of those are later edits.

## In the merge queue

`~/bin/mq-runner.sh` runs `stale-check.sh <merge-base tip head> <head>` on each PR after
merging it onto the tip and before its suites. A hit takes the PR out of the queue with
a comment naming the files. A PR that means to put an older version back (a restore,
a revert) names each file in one of its commit messages:

    Restores-older-copy: wails-gui/app.go

## Run by hand

```bash
bash scripts/merge-audit/test.sh
```

```bash
python3 -I scripts/merge-audit/audit.py origin/feat/iphone-companion "2026-11-14 16:00 -0800" /tmp/audit.md
```
