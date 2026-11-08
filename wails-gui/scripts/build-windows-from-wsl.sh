#!/usr/bin/env bash
set -euo pipefail

# Keep compilation, Node's native packages and Wails binding generation on
# Windows. Arguments cross the boundary as argv, never interpolated shell code.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
windows_git="$(command -v git.exe 2>/dev/null || true)"
windows_bash=""
for candidate in \
    "${ISOFORM_WINDOWS_BASH:-}" \
    "${windows_git:+$(dirname "$windows_git")/../bin/bash.exe}" \
    /mnt/[a-z]/software/Git/bin/bash.exe \
    /mnt/[a-z]/Program\ Files/Git/bin/bash.exe; do
    if [ -n "$candidate" ] && [ -f "$candidate" ]; then
        windows_bash="$candidate"
        break
    fi
done
if [ -z "$windows_bash" ]; then
    echo "[build-wails] Windows Git Bash not found. Install Git for Windows or set ISOFORM_WINDOWS_BASH to its bash.exe path." >&2
    exit 1
fi

entry="$(wslpath -w "${SCRIPT_DIR}/build-windows-native.sh")"
echo "[build-wails] WSL: forwarding to Windows Git Bash (${windows_bash})"
exec "$windows_bash" --noprofile --norc -c \
    'exec bash "$(cygpath -u "$1")" "${@:2}"' isoform-windows-build "$entry" "$@"
