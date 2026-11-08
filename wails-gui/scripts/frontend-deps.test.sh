#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/frontend-deps.sh"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT
frontend="${fixture}/frontend with spaces"
cache="${fixture}/cache"
mkdir -p "${frontend}/node_modules"
printf original > "${frontend}/node_modules/unmarked"

# An unknown install is kept intact; pnpm gets a missing modules directory.
prepare_frontend_deps "$frontend" "$cache" win32-x64
[ ! -e "${frontend}/node_modules" ]
[ "$(cat "${cache}"/unmarked-*/unmarked)" = original ]
mkdir -p "${frontend}/node_modules"
printf windows > "${frontend}/node_modules/native-package"

# A second Windows build reuses exactly the existing installation.
prepare_frontend_deps "$frontend" "$cache" win32-x64
[ "$(cat "${frontend}/node_modules/native-package")" = windows ]
[ ! -e "${cache}/win32-x64" ]

# Switching systems saves both installations and restores their links in place.
prepare_frontend_deps "$frontend" "$cache" linux-x64
[ ! -e "${frontend}/node_modules" ]
[ "$(cat "${cache}/win32-x64/native-package")" = windows ]
mkdir -p "${frontend}/node_modules"
printf linux > "${frontend}/node_modules/native-package"
prepare_frontend_deps "$frontend" "$cache" win32-x64
[ "$(cat "${frontend}/node_modules/native-package")" = windows ]
[ "$(cat "${cache}/linux-x64/native-package")" = linux ]
prepare_frontend_deps "$frontend" "$cache" linux-x64
[ "$(cat "${frontend}/node_modules/native-package")" = linux ]
printf 'Frontend dependency reuse and OS switching passed.\n'
