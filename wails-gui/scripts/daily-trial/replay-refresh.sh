#!/bin/bash
# replay-refresh.sh: the daily replay's launchd entry. Before each run it brings
# its tools up to the branch tip (default feat/iphone-companion, or
# ISOFORM_REPLAY_BRANCH): it fetches the tip into its own clone, builds the
# count-only brief reader and copies replay-day.sh and replay.mjs. Any step
# that fails keeps the last good copy, so the replay never stops and never
# runs with half-updated tools. Then it runs the replay (read-only).
set -u
export PATH="/opt/homebrew/bin:/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:$PATH"
BASE="$HOME/isoform-trials"
BIN="$BASE/bin/replay"
SRC="$BASE/replay-src"
BRANCH="${ISOFORM_REPLAY_BRANCH:-feat/iphone-companion}"
REPO="${ISOFORM_REPLAY_REPO:-$HOME/src/new-desktop}"
mkdir -p "$BIN"
log() { echo "$(date '+%F %T') [replay-refresh] $*"; }

refresh() {
  if [ ! -d "$SRC/.git" ]; then
    git clone -q --no-checkout "$(git -C "$REPO" remote get-url origin)" "$SRC" || return 1
  fi
  git -C "$SRC" fetch -q origin "$BRANCH" || return 1
  git -C "$SRC" checkout -q --force FETCH_HEAD || return 1
  local tip; tip=$(git -C "$SRC" rev-parse --short HEAD)
  local tmp; tmp=$(mktemp -d "$BASE/.replay-refresh.XXXX")
  # Build into a scratch folder; only a complete set replaces the last good one.
  if ! ( cd "$SRC/wails-gui" && GOFLAGS=-p=2 nice -n 10 go build -o "$tmp/brief-replay" ./cmd/brief-replay ); then
    rm -rf "$tmp"; return 1
  fi
  cp "$SRC/wails-gui/scripts/daily-trial/replay-day.sh" "$SRC/wails-gui/scripts/daily-trial/replay.mjs" "$tmp/" || { rm -rf "$tmp"; return 1; }
  node --check "$tmp/replay.mjs" && bash -n "$tmp/replay-day.sh" || { rm -rf "$tmp"; return 1; }
  mv "$tmp/brief-replay" "$BASE/bin/brief-replay"
  mv "$tmp/replay-day.sh" "$tmp/replay.mjs" "$BIN/"
  chmod +x "$BIN/replay-day.sh"
  rm -rf "$tmp"
  echo "$tip" > "$BIN/source-commit"
  log "tools refreshed from $BRANCH at $tip"
}

refresh || log "refresh from $BRANCH failed; running the last good copy ($(cat "$BIN/source-commit" 2>/dev/null || echo unknown))"
exec bash "$BIN/replay-day.sh" "$@"
