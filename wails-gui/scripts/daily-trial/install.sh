#!/bin/bash
# install.sh trial|replay: installs the launchd job for one of the two daily runs.
#   trial   on the Mac mini: 05:00 every day, daily-trial.sh (copied to ~/isoform-trials/bin)
#   replay  on the person's computer: 04:30 every day, replay-day.sh from this checkout
# Run it once on the machine it is for. Uninstall: launchctl bootout gui/$UID/<label>; rm the plist.
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
kind="${1:-}"
case "$kind" in
  trial)  label=ai.isoform.daily-trial;  hour=5; minute=0
          mkdir -p "$HOME/isoform-trials/bin"; cp "$HERE/daily-trial.sh" "$HOME/isoform-trials/bin/daily-trial.sh"
          program="$HOME/isoform-trials/bin/daily-trial.sh" ;;
  replay) label=ai.isoform.daily-replay; hour=4; minute=30; program="$HERE/replay-day.sh" ;;
  *) echo "usage: install.sh trial|replay" >&2; exit 2 ;;
esac
plist="$HOME/Library/LaunchAgents/$label.plist"
# ISOFORM_TRIAL_BRANCH at install time pins the branch the trial builds
# (default feat/iphone-companion), for a branch not merged yet.
env_block=""
if [ -n "${ISOFORM_TRIAL_BRANCH:-}" ]; then
  env_block="<key>EnvironmentVariables</key><dict><key>ISOFORM_TRIAL_BRANCH</key><string>$ISOFORM_TRIAL_BRANCH</string></dict>"
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
  <key>Nice</key><integer>5</integer>
</dict></plist>
PLIST
launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$plist"
echo "installed $label at $(printf %02d:%02d "$hour" "$minute") daily: $plist"
