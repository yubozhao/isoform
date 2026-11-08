#!/usr/bin/env bash

# node_modules contains native packages and OS-specific links. Keep a cache for
# each Node host, restoring it at its original path before pnpm checks the lock.
prepare_frontend_deps() {
    local frontend="$1" cache="$2" host="$3" modules marker previous saved
    modules="${frontend}/node_modules"
    marker="${cache}/current-host"
    if [ -d "$modules" ]; then
        previous=""
        if [ -f "$marker" ]; then
            previous="$(cat "$marker")"
        fi
        if [ "$previous" = "$host" ]; then
            echo "[build-wails] Reusing ${host} frontend dependencies: ${frontend}"
            return 0
        fi
        mkdir -p "$cache"
        # Older installs have no marker. Preserve them once rather than letting
        # Windows pnpm try to delete WSL symlinks, which fails with EACCES.
        case "$previous" in
            win32-x64|win32-arm64|win32-ia32|linux-x64|linux-arm64|linux-arm|darwin-x64|darwin-arm64)
                saved="${cache}/${previous}"
                ;;
            *) saved="${cache}/unmarked-$(date +%s)-$$" ;;
        esac
        if [ -e "$saved" ]; then
            saved="${saved}-previous-$(date +%s)-$$"
        fi
        echo "[build-wails] Saving frontend dependencies to ${saved}"
        mv "$modules" "$saved"
    fi
    if [ -d "${cache}/${host}" ]; then
        echo "[build-wails] Restoring ${host} frontend dependencies: ${frontend}"
        mv "${cache}/${host}" "$modules"
    else
        echo "[build-wails] Preparing ${host} frontend dependencies: ${frontend}"
    fi
    mkdir -p "$cache"
    printf '%s\n' "$host" > "$marker"
}
