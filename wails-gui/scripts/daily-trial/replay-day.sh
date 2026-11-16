#!/bin/bash
# replay-day.sh: the daily read-only replay of the previous day on the person's own computer
# (docs/rework-2026-11.md, step 4, run 2). It reads, never writes, the app's data:
#   - the stored briefs through cmd/brief-replay (counts only: narrative, cards, evidence, audio)
#   - yansu-gui.log and its rotated copies: first paints, binding timings, error counts by tag,
#     [chat-turn] outcomes. Numbers and tags only; no message, title or log text is kept.
#   - answers/ledger.jsonl: judgments per source per day (counts only, judgments.mjs)
# It compares the day with the one before (its own saved metrics) and writes
# ~/isoform-trials/replay/<date>.md, <date>.json and latest.txt (one line).
set -u
export PATH="/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
HERE="$(cd "$(dirname "$0")" && pwd)"
WAILS="$(cd "$HERE/../.." && pwd)"
DATA="${ISOFORM_REPLAY_DATA:-$HOME/.isoform-agent}"
OUT="${ISOFORM_REPLAY_OUT:-$HOME/isoform-trials/replay}"
DAY="${1:-$(date -v-1d +%Y-%m-%d)}"
mkdir -p "$OUT" "$HOME/isoform-trials/bin"

# The count-only brief reader, built once per source change (small, no app build).
BR="$HOME/isoform-trials/bin/brief-replay"
if [ ! -x "$BR" ] || [ -n "$(find "$WAILS/cmd/brief-replay" -newer "$BR" -name '*.go' 2>/dev/null | head -1)" ]; then
  ( cd "$WAILS" && GOFLAGS=-p=2 go build -o "$BR" ./cmd/brief-replay ) || echo "brief-replay build failed" >&2
fi
BRIEFS="$OUT/$DAY.briefs.txt"
"$BR" -data "$DATA" 2>/dev/null | grep -E "^$DAY-" > "$BRIEFS" || true

LOGS=()
for f in "$DATA/yansu-gui.log.old.2" "$DATA/yansu-gui.log.old" "$DATA/yansu-gui.log"; do [ -f "$f" ] && LOGS+=("$f"); done
# The answers ledger: only ids, actions, sources and times are read (judgments.mjs).
ISOFORM_REPLAY_LEDGER="$DATA/answers/ledger.jsonl" node "$HERE/replay.mjs" "$DAY" "$OUT" "$BRIEFS" "${LOGS[@]}"
rm -f "$BRIEFS"
