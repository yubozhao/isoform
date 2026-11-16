#!/usr/bin/env python3
"""audit.py <ref> <since> <report.md>: audit the merges into <ref> since <since>.

Run from inside the repository (python3 -I, standard library only). Writes a
Markdown report and prints one summary line, then one line per finding that
starts with REGRESSION. Exit status: 0 clean, 1 at least one REGRESSION,
2 the audit could not run.

A REGRESSION is a change that undid or dropped what feat already had:
  - merge: a merge commit whose tree differs from the merge git makes on its
    own (`git merge-tree --write-tree P1 P2`) although that merge has no
    conflict, so something was edited by hand in the merge commit;
  - stale: the PR head carries an older copy of a file (stale-check.sh), as
    7c2df4fe6 (#1034) did. A later PR that puts back the version such a merge
    overwrote (as #1038 did) is a restore and goes to the report instead.
Everything else is listed in the report for a person to read, not counted:
merges with a hand-resolved conflict, feat-into-branch merges whose
resolution changed a file with no conflict or left out lines one side added,
and lines added by a merge in the window that are gone at the tip (most are
later edits; the report names the merge that removed each group).
"""
import collections
import os
import re
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
MIN_LINE = 25  # shorter added lines are too common to trace


def git(*args, check=True):
    r = subprocess.run(["git", *args], capture_output=True, text=True, errors="replace")
    if check and r.returncode != 0:
        raise RuntimeError("git %s: %s" % (" ".join(args), r.stderr.strip()))
    return r.stdout


def merge_tree(p1, p2):
    """(tree, conflicted paths) of the merge git makes on its own."""
    r = subprocess.run(
        ["git", "merge-tree", "--write-tree", "--name-only", p1, p2],
        capture_output=True,
        text=True,
        errors="replace",
    )
    lines = r.stdout.split("\n")
    tree = lines[0].strip()
    conflicted = []
    if r.returncode == 1:
        for line in lines[1:]:
            if not line.strip():
                break
            conflicted.append(line.strip())
    elif r.returncode != 0:
        raise RuntimeError("merge-tree %s %s: %s" % (p1, p2, r.stderr.strip()))
    return tree, sorted(set(conflicted))


_cache = {}


def lines_at(commit, path):
    key = (commit, path)
    if key not in _cache:
        r = subprocess.run(
            ["git", "show", "%s:%s" % (commit, path)],
            capture_output=True,
            text=True,
            errors="replace",
        )
        _cache[key] = (
            None if r.returncode else set(x.strip() for x in r.stdout.splitlines())
        )
    return _cache[key]


def added_lines(a, b, path=None):
    """{path: [stripped added lines of MIN_LINE+ characters]} in git diff a b."""
    args = ["diff", "--no-renames", "-U0", a, b]
    if path:
        args += ["--", path]
    out = collections.defaultdict(list)
    cur = None
    for line in git(*args).splitlines():
        if line.startswith("+++ "):
            cur = line[6:] if line.startswith("+++ b/") else None
        elif cur and line.startswith("+") and not line.startswith("+++"):
            s = line[1:].strip()
            if len(s) >= MIN_LINE and not re.fullmatch(r"[\W_]+", s):
                out[cur].append(s)
    return out


def pr_label(merge):
    body = git("log", "-1", "--format=%s%n%b", merge)
    m = re.search(r"^#(\d+):", body, re.M) or re.search(r"pull request #(\d+)", body)
    branch = re.search(r"Merge branch 'origin/([^']+)'", body) or re.search(
        r"from \S+?/(\S+)", body
    )
    parts = []
    if m:
        parts.append("#" + m.group(1))
    if branch:
        parts.append(branch.group(1))
    return " ".join(parts) or merge[:9]


def main():
    if len(sys.argv) != 4:
        print("usage: audit.py <ref> <since> <report.md>", file=sys.stderr)
        return 2
    ref, since, report_path = sys.argv[1:]
    rows = [
        l.split()
        for l in git(
            "log", ref, "--first-parent", "--since=" + since, "--format=%H %P"
        ).splitlines()
    ]
    merges = [r for r in reversed(rows) if len(r) == 3]  # oldest first
    tip = git("rev-parse", ref).strip()
    regressions, notes = [], []
    stale_seen = set()  # (merge, path) of stale hits, to recognise their restores
    report = [
        "# Merge audit: %s since %s" % (ref, since),
        "",
        "Tip %s, %d merges." % (tip[:9], len(merges)),
        "",
    ]

    for m, p1, p2 in merges:
        label = pr_label(m)
        tree, conflicted = merge_tree(p1, p2)
        changed = [f for f in git("diff", "--name-only", tree, m).splitlines() if f]
        if conflicted:
            notes.append(
                "%s %s: conflict resolved by hand in the merge commit (%s)"
                % (m[:9], label, ", ".join(conflicted))
            )
        extra = [f for f in changed if f not in conflicted]
        if extra:
            regressions.append(
                "merge %s %s changed files beyond the PR: %s"
                % (m[:9], label, ", ".join(extra))
            )

        base = git("merge-base", p1, p2).strip()
        r = subprocess.run(
            ["bash", os.path.join(HERE, "stale-check.sh"), base, p2],
            capture_output=True,
            text=True,
            errors="replace",
        )
        if r.returncode == 1:
            for hit in r.stdout.splitlines():
                path, before, subject = (hit.split(" ", 2) + ["", ""])[:3]
                if (before, path) in stale_seen:
                    # Puts back the version an earlier stale merge overwrote: a restore.
                    notes.append(
                        "%s %s: restores %s as it was before the stale merge %s"
                        % (m[:9], label, path, before)
                    )
                    continue
                stale_seen.add((m[:9], path))
                regressions.append(
                    "stale %s %s: %s is the version before %s (%s)"
                    % (m[:9], label, path, before, subject)
                )
        elif r.returncode != 0:
            raise RuntimeError("stale-check %s: %s" % (m[:9], r.stderr.strip()))

        # feat-into-branch merges inside the PR branch
        for c in git("rev-list", "--merges", "%s..%s" % (p1, p2)).split():
            parents = git("rev-list", "--parents", "-n1", c).split()[1:]
            if len(parents) != 2:
                continue
            a, f = parents
            itree, iconf = merge_tree(a, f)
            ichanged = [
                x for x in git("diff", "--name-only", itree, c).splitlines() if x
            ]
            iextra = [x for x in ichanged if x not in iconf]
            if iextra:
                notes.append(
                    "%s %s: branch merge %s changed files with no conflict: %s"
                    % (m[:9], label, c[:9], ", ".join(iextra))
                )
            ibase = git("merge-base", a, f).strip()
            for path in iconf:
                have = lines_at(c, path)
                for side, head in (("feat", f), ("branch", a)):
                    lost = [
                        s
                        for s in added_lines(ibase, head, path).get(path, [])
                        if have is None or s not in have
                    ]
                    if lost:
                        notes.append(
                            "%s %s: branch merge %s left out %d %s-side line(s) in %s, e.g. `%s`"
                            % (m[:9], label, c[:9], len(lost), side, path, lost[0][:80])
                        )

    # Lines added in the window that are gone at the tip.
    gone = collections.defaultdict(list)
    for i, (m, p1, _) in enumerate(merges):
        for path, lines in added_lines(p1, m).items():
            at_tip = lines_at(tip, path)
            for s in lines:
                if at_tip is not None and s in at_tip:
                    continue
                remover = "?"
                for m2, _, _ in merges[i + 1 :]:
                    c = lines_at(m2, path)
                    if c is None or s not in c:
                        remover = m2
                        break
                gone[(m, path, remover, at_tip is None)].append(s)
    for (m, path, remover, file_gone), lines in gone.items():
        notes.append(
            "%s %s: %d added line(s) in %s %s by %s, e.g. `%s`"
            % (
                m[:9],
                pr_label(m),
                len(lines),
                path,
                "deleted with the file" if file_gone else "gone",
                (
                    "%s %s" % (remover[:9], pr_label(remover))
                    if remover != "?"
                    else "a later change"
                ),
                lines[0][:80],
            )
        )

    report += (
        ["## Regressions", ""] + (["- " + x for x in regressions] or ["None."]) + [""]
    )
    report += (
        ["## For a person to read", ""]
        + (["- " + x for x in notes] or ["Nothing."])
        + [""]
    )
    with open(report_path, "w") as fh:
        fh.write("\n".join(report))

    print(
        "%s %d merges to %s: %d regressions, %d notes (%s)"
        % (
            "FOUND" if regressions else "OK",
            len(merges),
            tip[:9],
            len(regressions),
            len(notes),
            report_path,
        )
    )
    for x in regressions:
        print("REGRESSION " + x)
    return 1 if regressions else 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Exception as exc:  # the log must say the audit did not run
        print("ERROR audit did not run: %s" % exc)
        sys.exit(2)
