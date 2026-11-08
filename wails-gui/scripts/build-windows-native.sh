#!/usr/bin/env bash
set -euo pipefail

# Bootstrap common Windows tool locations without depending on WSL's PATH.
# ISOFORM_WINDOWS_BUILD_PATH accepts additional Git Bash paths separated by :.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
windows_profile="$(cygpath -u "${USERPROFILE:?Windows USERPROFILE is missing}")"
tools_root="$(cygpath -u "$(dirname "$(cygpath -m /)")")"
for candidate in \
    "${tools_root}/Go/bin" "${tools_root}/go/bin" \
    "${tools_root}/mingw64/bin" "${tools_root}/nodejs" \
    "/c/Program Files/Go/bin" "/c/Program Files/nodejs" \
    "/c/msys64/mingw64/bin" "/c/msys64/ucrt64/bin" \
    "${windows_profile}/go/bin" "${windows_profile}/AppData/Roaming/npm"; do
    if [ -d "$candidate" ]; then
        export PATH="${candidate}:${PATH}"
    fi
done
export PATH="${ISOFORM_WINDOWS_BUILD_PATH:+${ISOFORM_WINDOWS_BUILD_PATH}:}${PATH}"

for executable in go.exe node.exe wails.exe pnpm gcc g++; do
    if ! command -v "$executable" >/dev/null 2>&1; then
        echo "[build-wails] Missing Windows tool: ${executable}. Add its directory to Windows PATH or ISOFORM_WINDOWS_BUILD_PATH." >&2
        exit 1
    fi
done
if [ "$(node -p 'process.platform' | tr -d '\r')" != win32 ]; then
    echo "[build-wails] Windows builds require Windows Node.js." >&2
    exit 1
fi
export CC="${CC:-gcc}" CXX="${CXX:-g++}" GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"
exec bash "${SCRIPT_DIR}/build-wails.sh" "$@"
