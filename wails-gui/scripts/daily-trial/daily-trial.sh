#!/bin/bash
# daily-trial.sh: the daily real-app test (docs/rework-2026-11.md, step 4, run 1).
# Runs on the Mac mini every morning (launchd, see install-mini.sh):
#   1. check out the feat tip into ~/isoform-trials/tip and build it with build-wails.sh
#   2. seed a fresh test data folder with a scripted day (TestTrialSeed); no one's real data
#   3. launch the built app on that folder with the browser bridge on (port 34116)
#   4. drive every core page with Playwright (e2e/daily-real-app.spec.ts): paint budget,
#      screenshot, no errors
#   5. read the app's log for first-paint times and errors, quit the app
#   6. write ~/isoform-trials/daily/<date>.md and one line to ~/isoform-trials/daily/latest.txt
# Never touches ~/.isoform-agent or ~/.yansu-agent, and only stops the PIDs it started.
set -u
export PATH="$HOME/.local/bin:$HOME/go/bin:/opt/homebrew/bin:/opt/homebrew/opt/node@22/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
BASE="$HOME/isoform-trials"
REPO="${ISOFORM_TRIAL_REPO:-$HOME/src/isoform}"
BRANCH="${ISOFORM_TRIAL_BRANCH:-feat/iphone-companion}"
TIP="$BASE/tip"
DATE="$(date +%Y-%m-%d)"
RUN="$BASE/daily/$DATE"
DATA="$RUN/data"
REPORT="$BASE/daily/$DATE.md"
LATEST="$BASE/daily/latest.txt"
BRIDGE="http://127.0.0.1:34116"
mkdir -p "$BASE/daily" "$BASE/baseline"
rm -rf "$RUN"; mkdir -p "$RUN"
exec > >(tee "$RUN/run.log") 2>&1

result() { # result PASS|FAIL "one line"
  local status="$1" line="$2"
  echo "$DATE $status: $line" > "$LATEST"
  echo "result: $status: $line"
}
APP_PID=""
cleanup() {
  [ -n "$APP_PID" ] || return
  kill -TERM "$APP_PID" 2>/dev/null
  for _ in $(seq 1 20); do kill -0 "$APP_PID" 2>/dev/null || break; sleep 1; done
  kill -0 "$APP_PID" 2>/dev/null && kill -KILL "$APP_PID" 2>/dev/null
  # Children inside our own bundle only (helpers it started).
  pgrep -f "$TIP/wails-gui/build/bin/Isoform.app" | while read -r p; do kill -TERM "$p" 2>/dev/null; done
  APP_PID=""
}
trap cleanup EXIT

# 1. Tip and build.
git -C "$REPO" fetch -q origin "$BRANCH" || { result FAIL "git fetch failed"; exit 1; }
if [ ! -e "$TIP/.git" ]; then git -C "$REPO" worktree add -q --detach "$TIP" "origin/$BRANCH" || { result FAIL "worktree add failed"; exit 1; }; fi
git -C "$TIP" checkout -q -f --detach "origin/$BRANCH" && git -C "$TIP" clean -qfd -e wails-gui/frontend/node_modules
SHA="$(git -C "$TIP" rev-parse --short HEAD)"
echo "tip=$SHA"
if [ ! -d "$TIP/wails-gui/frontend/node_modules" ] && [ -d "$REPO/wails-gui/frontend/node_modules" ]; then
  cp -cR "$REPO/wails-gui/frontend/node_modules" "$TIP/wails-gui/frontend/"
fi
# The mini's global npm (11) and pnpm (12) differ from the MacBook's; pin what the repo builds with,
# and the go toolchain go.mod asks for (the mini's default go is newer than Wails accepts).
mkdir -p "$BASE/shim"
printf '#!/bin/bash\nexec /opt/homebrew/bin/npx -y npm@10.9.4 "$@"\n' > "$BASE/shim/npm"
printf '#!/bin/bash\nexec /opt/homebrew/bin/npx -y pnpm@10.8.1 "$@"\n' > "$BASE/shim/pnpm"
chmod +x "$BASE/shim/npm" "$BASE/shim/pnpm"
export PATH="$HOME/.local/bin:$BASE/shim:$PATH" GOTOOLCHAIN="${ISOFORM_TRIAL_GOTOOLCHAIN:-go1.25.11}"
BUILD_START=$(date +%s)
# ISOFORM_TRIAL_REUSE_BUILD=1 keeps the last build, for working on the spec
# only; the scheduled run always builds.
if [ "${ISOFORM_TRIAL_REUSE_BUILD:-}" = 1 ] && [ -x "$TIP/wails-gui/build/bin/Isoform.app/Contents/MacOS/Yansu" ]; then
  echo "reused the last build" > "$RUN/build.log"; BUILD_RC=0
else
  ( cd "$TIP" && ./wails-gui/scripts/build-wails.sh --flavor isoform --keep-permissions ) > "$RUN/build.log" 2>&1
  BUILD_RC=$?
fi
BUILD_SECS=$(( $(date +%s) - BUILD_START ))
APP="$TIP/wails-gui/build/bin/Isoform.app"
if [ "$BUILD_RC" -ne 0 ] || [ ! -x "$APP/Contents/MacOS/Yansu" ]; then
  { echo "# Daily real-app test $DATE"; echo; echo "FAIL: the build failed on $SHA (rc=$BUILD_RC). See $RUN/build.log."; } > "$REPORT"
  result FAIL "build failed on $SHA"; exit 1
fi

# 2. Seed the scripted day into a fresh folder.
( cd "$TIP/wails-gui" && ISOFORM_TRIAL_SEED_DIR="$DATA" go test -run 'TestTrialSeed$' -count=1 . ) > "$RUN/seed.log" 2>&1 \
  || { { echo "# Daily real-app test $DATE"; echo; echo "FAIL: seeding the test data failed on $SHA. See $RUN/seed.log."; } > "$REPORT"; result FAIL "seed failed on $SHA"; exit 1; }

# 3. Launch on the test folder with the bridge on. This app is ours alone: its own data folder.
LAUNCH_AT=$(date +%s)
env YANSU_APP_FLAVOR=isoform YANSU_AGENT_HOME="$DATA" YANSU_BROWSER_BRIDGE=1 \
  "$APP/Contents/MacOS/Yansu" > "$RUN/app.stdout" 2>&1 < /dev/null &
APP_PID=$!
echo "pid=$APP_PID"
up=0
for _ in $(seq 1 90); do
  curl -sf "$BRIDGE/api/health" >/dev/null 2>&1 && { up=1; break; }
  kill -0 "$APP_PID" 2>/dev/null || break
  sleep 2
done
if [ "$up" -ne 1 ]; then
  { echo "# Daily real-app test $DATE"; echo; echo "FAIL: the app on $SHA did not start (bridge never answered). See $RUN/app.stdout."; } > "$REPORT"
  result FAIL "app did not start on $SHA"; exit 1
fi
STARTUP_SECS=$(( $(date +%s) - LAUNCH_AT ))

# 4. Drive every core page.
SPEC_RC=0
( cd "$TIP/wails-gui/frontend" && npx playwright install chromium ) > "$RUN/playwright-install.log" 2>&1 || true
( cd "$TIP/wails-gui/frontend" && DAILY_TRIAL_OUT="$RUN" DAILY_TRIAL_BASELINE="$BASE/baseline" DAILY_TRIAL_BRIDGE="$BRIDGE" \
  npx playwright test --config playwright.daily.config.ts --update-snapshots=missing --reporter=json > "$RUN/playwright.json" 2> "$RUN/playwright.stderr" ) || SPEC_RC=$?

# Quick chat: one prompt through the helper's own route to the app.
node "$TIP/wails-gui/scripts/daily-trial/quick-chat.mjs" "$APP" "$RUN" > "$RUN/quick-chat.log" 2>&1 || true

# 5. The app's own log: first paints and errors since launch.
cleanup
LOG="$DATA/yansu-gui.log"
grep -E '\[perf\] first_paint page=' "$LOG" 2>/dev/null | sed -E 's/.*first_paint page=([a-z_-]+) ms=([0-9]+).*/\1 \2/' > "$RUN/first-paint.txt"
grep -iE 'panic|fatal|\[error\]| error[:=]|failed' "$LOG" 2>/dev/null | grep -vE "$(cat "$TIP/wails-gui/scripts/daily-trial/known-noise.txt" 2>/dev/null | grep -v '^#' | paste -sd'|' - || echo '^$')" > "$RUN/log-errors.txt" || true
ERRORS=$(wc -l < "$RUN/log-errors.txt" | tr -d ' ')
# How each brief read went (shown, or withheld and why): counts only.
grep -oE '\[mobile-brief\] read brief=[^ ]+ written=[a-z]+ reason=[a-z_]+' "$LOG" 2>/dev/null | sort | uniq -c > "$RUN/brief-reads.txt" || true

# Phone: pairing and the brief through a local relay. The existing end-to-end
# test builds the relay binary, pairs a phone that runs the production crypto
# with the hub code, and waits for a brief to reach it.
PHONE_AT=$(date +%s)
if ( cd "$TIP/wails-gui" && YANSU_MOBILE_E2E=1 go test -run 'TestMobileEndToEndThroughRelayBinary$' -count=1 -timeout 15m . ) > "$RUN/phone-relay.log" 2>&1; then
  PHONE_OK=true; PHONE_FAIL='[]'
else
  PHONE_OK=false
  PHONE_FAIL="[$(grep -m1 -E -- '--- FAIL|_test.go:[0-9]+:' "$RUN/phone-relay.log" | head -c 200 | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>process.stdout.write(JSON.stringify(s.trim()||"see phone-relay.log")))')]"
fi
echo "{\"page\":\"phone-relay\",\"ok\":$PHONE_OK,\"paintMs\":$(( ($(date +%s) - PHONE_AT) * 1000 )),\"budgetMs\":600000,\"checks\":[\"paired through the relay\",\"brief delivered\"],\"failures\":$PHONE_FAIL,\"screenshot\":\"skipped\"}" >> "$RUN/pages.jsonl"

# 6. Report.
node "$TIP/wails-gui/scripts/daily-trial/report.mjs" "$RUN" "$DATE" "$SHA" "$BUILD_SECS" "$STARTUP_SECS" "$ERRORS" "$BRANCH" > "$REPORT"
STATUS=$(head -1 "$RUN/summary.txt" 2>/dev/null || echo "FAIL report missing")
case "$STATUS" in
  PASS*) result PASS "${STATUS#PASS }" ;;
  *) result FAIL "${STATUS#FAIL }" ;;
esac
