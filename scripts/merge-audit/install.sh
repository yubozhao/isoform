#!/bin/bash
# install.sh: on the Mac mini, installs the merge audit.
#   - copies stale-check.sh, audit.py and audit.sh to ~/bin/merge-audit/
#     (the merge queue runner calls ~/bin/merge-audit/stale-check.sh before it merges a PR);
#   - installs the launchd job ai.isoform.merge-audit: audit.sh every day at 04:30.
# Run it again after changing any of them. Uninstall:
#   launchctl bootout gui/$UID/ai.isoform.merge-audit; rm ~/Library/LaunchAgents/ai.isoform.merge-audit.plist
set -eu
HERE="$(cd "$(dirname "$0")" && pwd)"
dest="$HOME/bin/merge-audit"; label=ai.isoform.merge-audit
mkdir -p "$dest" "$HOME/isoform-mq" "$HOME/Library/LaunchAgents"
for f in stale-check.sh audit.py audit.sh; do
  # A new file renamed into place: a runner reading the old one keeps it.
  cp "$HERE/$f" "$dest/.$f.new" && mv "$dest/.$f.new" "$dest/$f"
done
plist="$HOME/Library/LaunchAgents/$label.plist"
cat > "$plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>$label</string>
  <key>ProgramArguments</key><array><string>/bin/bash</string><string>$dest/audit.sh</string></array>
  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>4</integer><key>Minute</key><integer>30</integer></dict>
  <key>StandardOutPath</key><string>$HOME/isoform-mq/$label.out</string>
  <key>StandardErrorPath</key><string>$HOME/isoform-mq/$label.out</string>
  <key>LowPriorityIO</key><true/>
  <key>Nice</key><integer>10</integer>
</dict></plist>
PLIST
launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$plist"
echo "installed $label at 04:30 daily: $plist; scripts in $dest"
