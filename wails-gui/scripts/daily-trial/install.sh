#!/bin/bash
# install.sh trial|replay: installs the launchd job for one of the two daily runs.
#   trial   on the Mac mini: 05:00 every day, daily-trial.sh (copied to ~/isoform-trials/bin)
#   replay  on the person's computer: 05:30 every day, replay-refresh.sh (copied to ~/isoform-trials/bin/replay):
#           it refreshes its tools from the branch tip, keeping the last good copy, then runs replay-day.sh
# Run it once on the machine it is for. Uninstall: launchctl bootout gui/$UID/<label>; rm the plist.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
kind="${1:-}"
case "$kind" in
  trial)  label=ai.isoform.daily-trial;  hour=5; minute=0
          mkdir -p "$HOME/isoform-trials/bin"; cp "$HERE/daily-trial.sh" "$HOME/isoform-trials/bin/daily-trial.sh"
          program="$HOME/isoform-trials/bin/daily-trial.sh" ;;
  replay) label=ai.isoform.daily-replay; hour=5; minute=30
          mkdir -p "$HOME/isoform-trials/bin/replay"
          cp "$HERE/replay-refresh.sh" "$HERE/replay-day.sh" "$HERE/replay.mjs" "$HOME/isoform-trials/bin/replay/"
          program="$HOME/isoform-trials/bin/replay/replay-refresh.sh" ;;
  *) echo "usage: install.sh trial|replay" >&2; exit 2 ;;
esac
plist="$HOME/Library/LaunchAgents/$label.plist"
# ISOFORM_TRIAL_BRANCH (trial) or ISOFORM_REPLAY_BRANCH (replay) at install
# time pins the branch the job builds from (default feat/iphone-companion), for
# a branch not merged yet.
env_block=""
pin_name=ISOFORM_TRIAL_BRANCH; pin="${ISOFORM_TRIAL_BRANCH:-}"
if [ "$kind" = replay ]; then pin_name=ISOFORM_REPLAY_BRANCH; pin="${ISOFORM_REPLAY_BRANCH:-}"; fi
if [ -n "$pin" ]; then
  env_block="<key>EnvironmentVariables</key><dict><key>$pin_name</key><string>$pin</string></dict>"
fi
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/isoform-trials"
cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>$program</string></array>
  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>$hour</integer><key>Minute</key><integer>$minute</integer></dict>
  <key>StandardOutPath</key><string>$HOME/isoform-trials/$label.out</string>
  <key>StandardErrorPath</key><string>$HOME/isoform-trials/$label.out</string>
  $env_block
  <key>LowPriorityIO</key><true/>
  <key>Nice</key><integer>10</integer>
</dict></plist>
PLIST
launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$plist"
echo "installed $label at $(printf %02d:%02d "$hour" "$minute") daily: $plist"
