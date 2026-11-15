#!/usr/bin/env bash
set -euo pipefail

# ── Centralized launcher for the Wails GUI (wails-gui/) ──────────────
#
# Checks prerequisites, prepares runtime bundles if needed, then starts
# the Wails dev server (or triggers a production build).
#
# Usage:
#   wails-gui/scripts/start-wails-gui.sh                      # dev mode (default)
#   wails-gui/scripts/start-wails-gui.sh --skip-cli           # dev mode, skip bundled yansu CLI
#   wails-gui/scripts/start-wails-gui.sh --build              # production build (.app)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WAILS_DIR="$(dirname "$SCRIPT_DIR")"
WAILS_DIR_REAL="$(cd "$WAILS_DIR" && pwd -P)"
REPO_ROOT="$(dirname "$WAILS_DIR")"
FRONTEND_DIR="${WAILS_DIR}/frontend"
FRONTEND_DIR_REAL="${WAILS_DIR_REAL}/frontend"
CLI_BUNDLE="${WAILS_DIR}/yansu-cli-bundle"
CLI_ENTRY="${CLI_BUNDLE}/bin/yansu"
YANSU_AGENT_SRC="${REPO_ROOT}/yansu-agent"

# ── Load local dev env overrides ─────────────────────────────────────
# wails-gui/.env.local is the canonical place to set per-developer env
# the Go runtime needs (e.g. ISOFORM_DESKTOP_POSTHOG_KEY for telemetry.go, which
# the user's machine wouldn't otherwise have because VITE_PUBLIC_* only
# exists at build time). Each line must `export NAME=value`; lines are
# sourced verbatim into this shell. Gitignored.
if [ -f "${WAILS_DIR}/.env.local" ]; then
    # shellcheck disable=SC1090,SC1091
    set -a
    . "${WAILS_DIR}/.env.local"
    set +a
fi

if [ "$(uname -s)" = Darwin ]; then
    export MACOSX_DEPLOYMENT_TARGET=14.0
    export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
    export CGO_CXXFLAGS="${CGO_CXXFLAGS:-} -mmacosx-version-min=14.0"
    export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"
fi

BUN_BUNDLE="${WAILS_DIR}/bun-bundle"
BUN_ENTRY="${BUN_BUNDLE}/bin/bun"

CUA_BUNDLE="${WAILS_DIR}/cua-driver-bundle"
CUA_APP="${CUA_BUNDLE}/CuaDriver.app"
CUA_ENTRY="${CUA_APP}/Contents/MacOS/cua-driver"
CUA_SRC="${WAILS_DIR}/cua-driver-src"
SPOTLIGHT_DIR="${WAILS_DIR}/quick-chat-spotlight"

# ── Parse arguments ──────────────────────────────────────────────────

SKIP_CLI=false
SKIP_WHISPER=false # legacy no-op; whisper.cpp replaced by sherpa-onnx (in-process)
SKIP_BUN=false
SKIP_CUA=false
SKIP_SPOTLIGHT=false
KEEP_PERMISSIONS=false
MODE="dev"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-cli)
            SKIP_CLI=true
            shift
            ;;
        --keep-permissions)
            KEEP_PERMISSIONS=true
            shift
            ;;
        --skip-whisper)
            SKIP_WHISPER=true
            shift
            ;;
        --skip-bun)
            SKIP_BUN=true
            shift
            ;;
        --skip-cua)
            SKIP_CUA=true
            shift
            ;;
        --skip-spotlight)
            SKIP_SPOTLIGHT=true
            shift
            ;;
        --build)
            MODE="build"
            shift
            ;;
        -h|--help)
            cat <<'USAGE'
Usage: wails-gui/scripts/start-wails-gui.sh [options]

Options:
  --skip-cli           Skip bundled yansu CLI check
  --skip-whisper       (deprecated, no-op) whisper.cpp replaced by sherpa-onnx
  --skip-bun           Skip bun runtime download
  --skip-cua           Skip bundled cua-driver build (disables the cua_driver agent tool — live macOS app reads + computer-use)
  --skip-spotlight     Skip Quick Chat Spotlight helper build (Cmd+J / Alt+Space falls back to native panel)
  --keep-permissions   Keep the previous dev build's macOS privacy grants (by default they are cleared, since the new ad-hoc signature no longer matches them)
  --build              Build production .app instead of starting dev server
  -h, --help           Show this help message
USAGE
            exit 0
            ;;
        *)
            echo "Unknown option: $1 (use --help for usage)"
            exit 1
            ;;
    esac
done

# ── Helpers ──────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

ok()   { printf "  ${GREEN}%-10s${NC} %s\n" "$1" "$2"; }
warn() { printf "  ${YELLOW}%-10s${NC} %s\n" "$1" "$2"; }
fail() { printf "  ${RED}%-10s${NC} %s\n" "$1" "$2"; }
info() { printf "${CYAN}%s${NC}\n" "$1"; }

configure_go_proxy() {
    local default_proxy configured_proxy
    default_proxy="${YANSU_GOPROXY:-https://proxy.golang.org,direct}"
    configured_proxy="${GOPROXY:-}"

    case "$configured_proxy" in
        ""|*goproxy.io*)
            export GOPROXY="$default_proxy"
            ;;
        *)
            if [[ -n "${YANSU_GOPROXY:-}" ]]; then
                export GOPROXY="$YANSU_GOPROXY"
            fi
            ;;
    esac
}

configure_go_proxy

realpath_of_dir() {
    local dir="$1"
    if [[ -d "$dir" ]]; then
        (cd "$dir" && pwd -P)
    fi
}

pid_cwd_realpath() {
    local pid="$1"
    local cwd
    cwd="$(lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p' | head -1)"
    if [[ -n "$cwd" && -d "$cwd" ]]; then
        realpath_of_dir "$cwd"
    fi
}

pid_command() {
    local pid="$1"
    ps -p "$pid" -o command= 2>/dev/null || true
}

add_pid_unique() {
    local pid="$1"
    [[ -n "$pid" ]] || return 0
    [[ "$pid" =~ ^[0-9]+$ ]] || return 0
    for existing in "${MATCHING_PIDS[@]:-}"; do
        if [[ "$existing" == "$pid" ]]; then
            return 0
        fi
    done
    MATCHING_PIDS+=("$pid")
}

collect_existing_dev_stack_pids() {
    local pid cwd cmd parent

    MATCHING_PIDS=()

    while read -r pid; do
        [[ -n "$pid" ]] || continue
        cwd="$(pid_cwd_realpath "$pid")"
        if [[ "$cwd" == "$WAILS_DIR_REAL" ]]; then
            add_pid_unique "$pid"
        fi
    done < <(pgrep -f '(^|/)wails dev($| )' || true)

    while read -r pid; do
        [[ -n "$pid" ]] || continue
        cwd="$(pid_cwd_realpath "$pid")"
        if [[ "$cwd" == "$FRONTEND_DIR_REAL" ]]; then
            add_pid_unique "$pid"
            parent="$(ps -p "$pid" -o ppid= 2>/dev/null | tr -d ' ')"
            add_pid_unique "$parent"
        fi
    done < <(pgrep -f 'vite(/bin/vite\.js)?' || true)

    while read -r pid; do
        [[ -n "$pid" ]] || continue
        cmd="$(pid_command "$pid")"
        # wails dev builds Isoform.app (wails.json "name"); Yansu.app is the
        # bundle name of dev runs before the rename.
        if [[ "$cmd" == *"${WAILS_DIR}/build/bin/Isoform.app/Contents/MacOS/Yansu"* || "$cmd" == *"${WAILS_DIR_REAL}/build/bin/Isoform.app/Contents/MacOS/Yansu"* \
            || "$cmd" == *"${WAILS_DIR}/build/bin/Yansu.app/Contents/MacOS/Yansu"* || "$cmd" == *"${WAILS_DIR_REAL}/build/bin/Yansu.app/Contents/MacOS/Yansu"* ]]; then
            add_pid_unique "$pid"
            parent="$(ps -p "$pid" -o ppid= 2>/dev/null | tr -d ' ')"
            add_pid_unique "$parent"
        fi
    done < <(pgrep -f '(Isoform|Yansu)\.app/Contents/MacOS/Yansu' || true)
}

wait_for_pid_exit() {
    local pid="$1"
    local tries=0
    while kill -0 "$pid" 2>/dev/null; do
        tries=$((tries + 1))
        if [[ "$tries" -ge 40 ]]; then
            return 1
        fi
        sleep 0.25
    done
}

stop_existing_dev_stack() {
    local pid

    collect_existing_dev_stack_pids

    if [[ "${#MATCHING_PIDS[@]}" -eq 0 ]]; then
        return 0
    fi

    info "Stopping existing Wails dev stack for ${WAILS_DIR_REAL}..."
    for pid in "${MATCHING_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done

    for pid in "${MATCHING_PIDS[@]}"; do
        if ! wait_for_pid_exit "$pid"; then
            warn "Reap" "PID ${pid} did not exit after SIGTERM, forcing"
            kill -9 "$pid" 2>/dev/null || true
        fi
    done

    ok "Reap" "stopped ${#MATCHING_PIDS[@]} existing dev process(es)"
}

# write_spotlight_info_plist writes a minimal Info.plist for the native
# macOS spotlight .app. LSUIElement keeps the helper out of the Dock /
# app switcher; the bundle id + executable let LaunchServices treat it as
# its own background app rather than a second Yansu.app instance.
write_spotlight_info_plist() {
    local plist_path="$1" exe="$2"
    mkdir -p "$(dirname "$plist_path")"
    cat >"$plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key><string>${exe}</string>
    <key>CFBundleIdentifier</key><string>com.yansu.quick-chat-spotlight</string>
    <key>CFBundleName</key><string>Isoform Spotlight</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
    <key>CFBundleShortVersionString</key><string>0.1.0</string>
    <key>CFBundleVersion</key><string>0.1.0</string>
    <key>LSUIElement</key><true/>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSMicrophoneUsageDescription</key><string>Isoform uses the microphone for voice typing.</string>
</dict>
</plist>
EOF
}

# build_spotlight_native_macos builds the macOS spotlight as a native
# AppKit NSPanel app (no Wails — see run_darwin.go), packaged as a .app so
# LSUIElement applies and the non-activating panel doesn't steal focus.
build_spotlight_native_macos() {
    local bin_dir="$1" helper_name="$2"
    local app_dir="${bin_dir}/${helper_name}.app"
    local macos_dir="${app_dir}/Contents/MacOS"

    # Frontend dist must exist before `go build` (//go:embed frontend/dist).
    if ! (cd "${SPOTLIGHT_DIR}/frontend" && pnpm run build); then
        return 1
    fi
    mkdir -p "$macos_dir"
    if ! (cd "$SPOTLIGHT_DIR" && go build -o "${macos_dir}/${helper_name}" .); then
        return 1
    fi
    write_spotlight_info_plist "${app_dir}/Contents/Info.plist" "$helper_name"
    return 0
}

# Build the Quick Chat Spotlight helper into quick-chat-spotlight/build/bin/
# if missing or out of date. The supervisor's dev candidate paths
# (spotlight_supervisor.go: spotlightBinaryCandidates) cover both layouts —
# a native .app bundle on macOS, flat binary on Windows/Linux — so no copy
# step is needed in dev.
#
# Skipped when:
#   - --skip-spotlight is passed
#   - quick-chat-spotlight/ source dir is missing (older checkouts)
# A skipped or failed build is non-fatal: the global Alt+Space / ⌥⌥
# hotkey falls back to ShowQuickChatWindow when the helper isn't installed.
ensure_spotlight_helper() {
    if [[ "$SKIP_SPOTLIGHT" == true ]]; then
        warn "Spotlight" "Skipped (--skip-spotlight). Quick Chat will use the legacy fallback panel."
        return
    fi
    if [[ ! -d "$SPOTLIGHT_DIR" ]]; then
        warn "Spotlight" "quick-chat-spotlight/ missing; skipping"
        return
    fi

    local helper_name="quick-chat-spotlight"
    local is_darwin=false
    case "$(uname -s)" in
        Darwin) is_darwin=true ;;
        MINGW*|CYGWIN*|MSYS*) helper_name="${helper_name}.exe" ;;
    esac

    # $helper_bin is the bare path the supervisor's dev candidate probes
    # (spotlight_supervisor.go: spotlightBinaryCandidates → build/bin/<name>).
    # $artifact is what `wails build` actually produces: on macOS that is a
    # .app bundle, NOT a bare binary, so the freshness check must reference
    # the binary inside the bundle — otherwise the bare path is always
    # "missing" and we rebuild the helper (~20s) on every single launch.
    local bin_dir="${SPOTLIGHT_DIR}/build/bin"
    local helper_bin="${bin_dir}/${helper_name}"
    local artifact="$helper_bin"
    if [[ "$is_darwin" == true ]]; then
        artifact="${bin_dir}/${helper_name}.app/Contents/MacOS/${helper_name}"
    fi

    # Decide whether to (re)build. Mirrors the yansu-cli rebuild check:
    # rebuild when any source file is newer than the built artifact. Pruning
    # node_modules/build/dist keeps the find from chasing generated trees.
    local need_build=false
    if [[ ! -f "$artifact" ]]; then
        info "Spotlight helper missing. Building..."
        need_build=true
    elif [[ -n "$(find "$SPOTLIGHT_DIR" \
            \( -path "*/node_modules" -o -path "*/build" -o -path "*/dist" \) -prune -o \
            \( -name '*.go' -o -name '*.ts' -o -name '*.tsx' -o -name '*.css' -o -name '*.html' -o -name 'go.sum' -o -name 'package.json' \) \
            -newer "$artifact" -print -quit 2>/dev/null)" ]]; then
        info "Spotlight helper source changed since last build. Rebuilding..."
        need_build=true
    fi

    if [[ "$need_build" == true ]]; then
        # Helper has its own React frontend — install deps once before wails build.
        if [[ ! -d "${SPOTLIGHT_DIR}/frontend/node_modules" ]]; then
            info "Installing spotlight frontend dependencies..."
            if ! (cd "${SPOTLIGHT_DIR}/frontend" && pnpm install); then
                warn "Spotlight" "frontend pnpm install failed; skipping helper build"
                return
            fi
        fi

        if [[ "$is_darwin" == true ]]; then
            # macOS: native NSPanel shell (no Wails — see run_darwin.go).
            if ! build_spotlight_native_macos "$bin_dir" "$helper_name"; then
                warn "Spotlight" "native build failed; Cmd+J / Alt+Space will use the legacy fallback"
                return
            fi
        else
            # Windows/Linux: still a Wails app. frontend/wailsjs/ is
            # auto-generated from app.go bindings; let wails regenerate it so
            # the Vite build can resolve the runtime import.
            if ! (cd "$SPOTLIGHT_DIR" && "$WAILS_BIN" build); then
                warn "Spotlight" "wails build failed; Cmd+J / Alt+Space will use the legacy fallback"
                return
            fi
        fi
    fi

    # On macOS, mark the just-built helper as LSUIElement so it doesn't
    # claim a Dock tile when launched. Idempotent — running on an
    # up-to-date install harmlessly re-asserts the value.
    if [[ "$is_darwin" == true ]]; then
        local helper_plist="${bin_dir}/${helper_name}.app/Contents/Info.plist"
        if [[ -f "$helper_plist" ]]; then
            /usr/libexec/PlistBuddy -c "Add :LSUIElement bool true" "$helper_plist" 2>/dev/null ||
                /usr/libexec/PlistBuddy -c "Set :LSUIElement true" "$helper_plist" 2>/dev/null ||
                warn "Spotlight" "failed to mark helper as LSUIElement"
        fi
    fi

    # On macOS `wails build` writes only the .app bundle, but the supervisor's
    # dev candidate path is the bare build/bin/<name>. Stage the inner binary
    # there so the just-built helper is actually found at launch instead of
    # silently dropping into the legacy fallback panel. Guarded by -nt so we
    # only copy after an actual rebuild, never on an up-to-date launch.
    if [[ "$is_darwin" == true && -f "$artifact" ]]; then
        if [[ ! -f "$helper_bin" || "$artifact" -nt "$helper_bin" ]]; then
            if ! cp -f "$artifact" "$helper_bin"; then
                warn "Spotlight" "failed to stage helper binary; Cmd+J / Alt+Space will use the legacy fallback"
                return
            fi
            chmod +x "$helper_bin" 2>/dev/null || true
        fi
    fi

    if [[ -f "$helper_bin" ]]; then
        ok "Spotlight" "helper ready at ${helper_bin}"
    else
        warn "Spotlight" "helper binary missing after build; Cmd+J / Alt+Space will use the legacy fallback"
    fi
}

# Ensure the macOS dev icon exists at build/darwin/iconfile.icns.
# Wails dev reads Info.dev.plist (CFBundleIconFile=iconfile) and falls back
# to the default app icon when this file is missing.
ensure_macos_dev_icon() {
    if [[ "$(uname -s)" != "Darwin" ]]; then
        return
    fi

    # Compare against the version-controlled source, not build/appicon.png:
    # the latter is re-copied (without -p) on every dev start, so its mtime
    # is always "now" and would force an icns rebuild every run.
    local icon_src="${WAILS_DIR}/assets/appicon.png"
    local darwin_build_dir="${WAILS_DIR}/build/darwin"
    local icon_icns="${darwin_build_dir}/iconfile.icns"

    if [[ ! -f "$icon_src" ]]; then
        warn "Icon" "source image not found: ${icon_src}"
        return
    fi

    if ! command -v sips &>/dev/null || ! command -v iconutil &>/dev/null; then
        warn "Icon" "sips/iconutil unavailable, skipping macOS dev icon generation"
        return
    fi

    mkdir -p "$darwin_build_dir"

    # Regenerate only if missing or source image is newer.
    if [[ -f "$icon_icns" && "$icon_icns" -nt "$icon_src" ]]; then
        ok "Icon" "dev icon ready at ${icon_icns}"
        return
    fi

    bash "${SCRIPT_DIR}/build-macos-icon.sh"

    ok "Icon" "generated macOS dev icon at ${icon_icns}"
}

# ── 1. Environment checks ───────────────────────────────────────────

info "Checking prerequisites..."

MISSING=0

if command -v go &>/dev/null; then
    ok "go" "$(go version | sed 's/go version //')"
else
    fail "go" "not found — install from https://go.dev/dl/"
    MISSING=1
fi

if command -v node &>/dev/null; then
    ok "node" "$(node --version)"
else
    fail "node" "not found — install via nvm or https://nodejs.org/"
    MISSING=1
fi

if command -v pnpm &>/dev/null; then
    ok "pnpm" "$(pnpm --version)"
else
    # pnpm may be managed by corepack; try enabling it
    if command -v corepack &>/dev/null; then
        warn "pnpm" "not found, attempting corepack enable..."
        corepack enable pnpm 2>/dev/null && ok "pnpm" "$(pnpm --version)" || { fail "pnpm" "corepack enable failed"; MISSING=1; }
    else
        fail "pnpm" "not found — run: npm install -g pnpm"
        MISSING=1
    fi
fi

WAILS_BIN="${WAILS_BIN:-$(command -v wails 2>/dev/null || true)}"
if [[ -z "$WAILS_BIN" ]] && [[ -x "$HOME/go/bin/wails" ]]; then
    WAILS_BIN="$HOME/go/bin/wails"
fi
if [[ -n "$WAILS_BIN" ]]; then
    ok "wails" "$($WAILS_BIN version 2>&1 | head -1)"
else
    fail "wails" "not found — run: go install github.com/wailsapp/wails/v2/cmd/wails@latest"
    MISSING=1
fi

if [[ "$MISSING" -ne 0 ]]; then
    echo ""
    echo "Missing prerequisites. Please install the tools above and retry."
    exit 1
fi

echo ""

# ── Global bundle cache paths ─────────────────────────────────────────
# Built bundles are cached at ~/.cache/yansu/*-output/ so that multiple
# worktrees can share them without rebuilding from scratch.
BUN_BUNDLE_CACHE="${HOME}/.cache/yansu/bun-bundle-output"

# restore_from_cache <cache_dir> <dest_dir> <label>
# Copies a cached bundle into the worktree if available.
restore_from_cache() {
    local cache_dir="$1" dest_dir="$2" label="$3"
    if [[ -d "$cache_dir" ]]; then
        info "Restoring ${label} from cache: ${cache_dir}"
        cp -r "$cache_dir" "$dest_dir"
        return 0
    fi
    return 1
}

# bun_bundle_arch_ok <bun_binary>
# Returns 0 when the binary's arch matches uname -m; warns and removes the
# stale bundle directory on mismatch so the caller falls through to download.
bun_bundle_arch_ok() {
    local bun_bin expected_arch actual_arch
    bun_bin="$1"
    expected_arch="$(uname -m)"
    actual_arch="$(file -b "$bun_bin" 2>/dev/null)"

    case "$expected_arch" in
        arm64|aarch64)
            echo "$actual_arch" | grep -qE 'arm64|aarch64' && return 0 ;;
        x86_64|amd64)
            echo "$actual_arch" | grep -qE 'x86-64|x86_64' && return 0 ;;
        *)
            # Unknown host arch — skip the check rather than block.
            return 0 ;;
    esac

    warn "Bun" "arch mismatch — expected ${expected_arch}, got: ${actual_arch}"
    warn "Bun" "Deleting stale bundle so a correct one will be fetched."
    rm -rf "${BUN_BUNDLE}"
    return 1
}

# ── 2. Yansu CLI bundle preparation ──────────────────────────────────

if [[ "$SKIP_CLI" == true ]]; then
    warn "CLI" "Skipped (--skip-cli). Bundled yansu CLI may not be available."
elif [[ -f "$CLI_ENTRY" || -f "${CLI_ENTRY}.exe" ]]; then
    existing_entry="$CLI_ENTRY"
    [[ -f "${CLI_ENTRY}.exe" ]] && existing_entry="${CLI_ENTRY}.exe"
    # Rebuild when any yansu-agent Go source is newer than the bundled binary.
    # Without this, subcommands added after the bundle was last built (e.g.
    # `yansu activity`, `yansu memory`) silently go missing in dev mode.
    if [[ -d "$YANSU_AGENT_SRC" ]] && [[ -n "$(find "$YANSU_AGENT_SRC" -name '*.go' -newer "$existing_entry" -print -quit 2>/dev/null)" ]]; then
        info "Yansu CLI source changed since last build. Rebuilding..."
        bash "${SCRIPT_DIR}/build-yansu-cli.sh"
        ok "CLI" "bundle rebuilt at ${CLI_BUNDLE}"
    else
        ok "CLI" "bundle exists at ${CLI_BUNDLE}"
    fi
else
    info "Bundled yansu CLI not found. Building..."
    bash "${SCRIPT_DIR}/build-yansu-cli.sh"
    ok "CLI" "bundle built at ${CLI_BUNDLE}"
fi

# Voice transcription now uses sherpa-onnx loaded in-process via cgo (see
# wails-gui/asr_sherpa.go). The whisper.cpp bundle build was removed; SenseVoice
# model assets are downloaded lazily on first voice input by bundled_sherpa.go.

if [[ "$SKIP_BUN" == true ]]; then
    warn "Bun" "Skipped (--skip-bun). Gem app generation will not be available."
elif [[ -f "$BUN_ENTRY" ]] && bun_bundle_arch_ok "$BUN_ENTRY"; then
    ok "Bun" "bundle exists at ${BUN_BUNDLE}"
elif [[ ! -f "$BUN_ENTRY" ]] && restore_from_cache "$BUN_BUNDLE_CACHE" "$BUN_BUNDLE" "Bun" && bun_bundle_arch_ok "$BUN_ENTRY"; then
    ok "Bun" "restored from cache at ${BUN_BUNDLE}"
else
    # Bundle missing, stale, or wrong arch — download a fresh one.
    [[ -d "$BUN_BUNDLE" ]] && rm -rf "$BUN_BUNDLE"
    info "Bun bundle not found. Downloading..."
    bash "${SCRIPT_DIR}/download-bun.sh"
    ok "Bun" "bundle downloaded to ${BUN_BUNDLE}"
fi

# ── 2c. CuaDriver bundle preparation ─────────────────────────────────
#   The bundled cua-driver Swift daemon powers the `cua_driver` agent
#   tool (read-live-app-state + computer-use). Without this bundle,
#   NewCuaDriverDaemon() fails at app launch, the daemon never spawns,
#   and the agent's cua_driver tool returns a "daemon not reachable"
#   error — which earlier wording led the LLM to silently fall back to
#   activity-cli (historical OCR snapshots) for present-moment queries
#   that genuinely needed live AX reads. Build proactively to keep the
#   tool usable in dev. Build needs Xcode + Swift toolchain; first build
#   takes ~30s, subsequent runs are no-op when the bundle is up to date.

if [[ "$SKIP_CUA" == true ]]; then
    warn "CuaDriver" "Skipped (--skip-cua). cua_driver agent tool will be unavailable; live macOS app reads disabled."
elif [[ -d "$CUA_APP" ]] && [[ -x "$CUA_ENTRY" ]]; then
    # Mirror the CLI-bundle rebuild trigger: if any Swift source OR
    # LLM-facing skill doc under cua-driver-src/ is newer than the
    # bundled binary, the user has edited the daemon (e.g. tweaked AX
    # walker depth in Daemon.swift) or the skill instructions (SKILL.md,
    # WEB_APPS.md, …) and we need to rebuild — otherwise dev would
    # silently keep running the stale .app. The *.md arm matters because
    # build-cua-driver-bundle.sh `ditto`s cua-driver-src/Skills/ into the
    # bundle, so a SKILL.md edit only reaches the LLM after a rebuild;
    # without it, skill edits look applied but the daemon serves the old
    # doc. Binary mtime is a valid baseline for both arms since every
    # rebuild re-runs swiftc and refreshes it.
    if [[ -d "$CUA_SRC" ]] && [[ -n "$(find "$CUA_SRC" \( -name '*.swift' -o -name '*.md' \) -newer "$CUA_ENTRY" -print -quit 2>/dev/null)" ]]; then
        info "CuaDriver source (Swift or skill docs) changed since last build. Rebuilding..."
        bash "${SCRIPT_DIR}/build-cua-driver-bundle.sh"
        ok "CuaDriver" "bundle rebuilt at ${CUA_BUNDLE}"
    else
        ok "CuaDriver" "bundle exists at ${CUA_BUNDLE}"
    fi
else
    info "CuaDriver bundle not found. Building (requires Xcode)..."
    bash "${SCRIPT_DIR}/build-cua-driver-bundle.sh"
    ok "CuaDriver" "bundle built at ${CUA_BUNDLE}"
fi

echo ""

# ── 2d. isoform-front-link (the Isoform front, front/) ─────────────
#   Built with the toolchain pinned in front/rust-toolchain.toml; cargo is
#   incremental, so an unchanged tree costs a second. Without cargo the
#   script warns and the app runs without the front.
bash "${SCRIPT_DIR}/build-front-link.sh" || warn "Front link" "build failed; the Isoform front will be unavailable"

# ── 3. Icon preparation ────────────────────────────────────────────
#    Copy version-controlled icons from icons/ → build/ so Wails can find them.

mkdir -p "${WAILS_DIR}/build"
if [[ -f "${WAILS_DIR}/assets/appicon.png" ]]; then
    cp "${WAILS_DIR}/assets/appicon.png" "${WAILS_DIR}/build/appicon.png"
fi

if [[ "$MODE" == "dev" ]]; then
    ensure_macos_dev_icon
    echo ""
fi

# ── 4. Dev environment pre-flight ───────────────────────────────────
#    Ensure the environment is ready for reliable hot-reload.

if [[ "$MODE" == "dev" ]]; then
    stop_existing_dev_stack

    # Ensure frontend/dist exists for the go:embed directive in main.go.
    # wails dev creates this on first run, but if it is missing, Go
    # rebuilds triggered by the file watcher fail silently.
    FRONTEND_DIST="${WAILS_DIR}/frontend/dist"
    if [[ ! -d "$FRONTEND_DIST" ]]; then
        mkdir -p "$FRONTEND_DIST"
        echo '<!doctype html>' > "${FRONTEND_DIST}/index.html"
        ok "Embed" "created frontend/dist stub for go:embed"
    fi

    # Ensure frontend dependencies are installed.
    if [[ ! -d "${WAILS_DIR}/frontend/node_modules" ]]; then
        info "Installing frontend dependencies..."
        (cd "${WAILS_DIR}/frontend" && pnpm install)
        ok "Deps" "frontend dependencies installed"
    fi

    # Build the Quick Chat Spotlight helper if needed so that Cmd+J /
    # Alt+Space exercises the same Spotlight pill in dev that ships in
    # production, instead of dropping into the legacy fallback panel.
    ensure_spotlight_helper
    echo ""

    # Raise file descriptor limit if too low.
    # fsnotify (kqueue on macOS) needs one fd per watched file;
    # 120+ Go files + Vite can exhaust the default 256.
    CURRENT_FD_LIMIT=$(ulimit -n 2>/dev/null || echo 0)
    # Treat "unlimited" or any non-numeric value as effectively very high.
    if ! [[ "$CURRENT_FD_LIMIT" =~ ^[0-9]+$ ]]; then
        CURRENT_FD_LIMIT=999999
    fi
    MIN_FD_LIMIT=4096
    if [[ "$CURRENT_FD_LIMIT" -lt "$MIN_FD_LIMIT" ]]; then
        if ulimit -n "$MIN_FD_LIMIT" 2>/dev/null; then
            ok "ulimit" "raised file descriptor limit: ${CURRENT_FD_LIMIT} -> ${MIN_FD_LIMIT}"
        else
            warn "ulimit" "fd limit is ${CURRENT_FD_LIMIT} (recommended: ${MIN_FD_LIMIT}), could not raise"
        fi
    fi

    # Pin the Vite dev server to one port so Wails (frontend:dev:serverUrl=auto)
    # doesn't need to guess which HMR server to attach to. If the preferred
    # port is held (typically by another project's stale dev server), walk
    # forward until we find a free one instead of forcing the user to kill
    # a foreign process.
    preferred_vite_port="${VITE_PORT:-5173}"
    chosen_vite_port=""
    if command -v lsof &>/dev/null; then
        for ((candidate=preferred_vite_port; candidate<preferred_vite_port+20; candidate++)); do
            if ! lsof -nP -iTCP:"$candidate" -sTCP:LISTEN &>/dev/null; then
                chosen_vite_port="$candidate"
                break
            fi
        done
        if [[ -z "$chosen_vite_port" ]]; then
            fail "Port" "No free port in ${preferred_vite_port}-$((preferred_vite_port+19)). Close some dev servers and retry."
            lsof -nP -iTCP:"$preferred_vite_port" -sTCP:LISTEN || true
            exit 1
        fi
    else
        chosen_vite_port="$preferred_vite_port"
    fi
    export VITE_PORT="$chosen_vite_port"
    if [[ "$chosen_vite_port" != "$preferred_vite_port" ]]; then
        warn "Port" "${preferred_vite_port} is busy; using ${chosen_vite_port} instead"
    else
        ok "Port" "Vite will listen on ${chosen_vite_port}"
    fi

    echo ""
fi

# ── 5. Launch ────────────────────────────────────────────────────────

cd "$WAILS_DIR"

case "$MODE" in
    dev)
        # Assemble dev flags.
        #   -m : skip `go mod tidy` before each compile. wails runs it on
        #        every start by default; go.mod rarely changes mid-session,
        #        so skipping it shaves a few seconds per launch.
        #   -ldflags="-w" (only when WAILS_FAST=1): drop DWARF debug info so
        #        the linker skips macOS dsymutil — the slowest part of the
        #        cgo link (onnxruntime + 30+ ObjC bridges into a ~54MB
        #        binary). This disables Go-level (delve) backend debugging,
        #        so it's opt-in; unset WAILS_FAST for a debuggable build.
        dev_args=(dev -m -debounce "${WAILS_DEBOUNCE:-300}")
        if [[ "${WAILS_FAST:-}" == "1" ]]; then
            dev_args+=(-ldflags "-w")
            ok "Fast" "WAILS_FAST=1 — stripping DWARF (faster link, no delve backend debugging)"
        fi

        # Development flavor: the dev app runs as Isoform with data in
        # ~/.isoform-agent, the release's identity, and never checks for
        # updates. Info.dev.plist carries the matching bundle name and
        # identifier.
        export YANSU_APP_FLAVOR="${YANSU_APP_FLAVOR:-isoform}"
        ok "Flavor" "running as ${YANSU_APP_FLAVOR} (data folder ~/.${YANSU_APP_FLAVOR}-agent)"
        # Pre-bundle the frontend's dependencies. When Vite discovers new
        # dependencies while the native window is already loading, it restarts
        # its server for a moment; the window then shows a blank page and the
        # reload signal does not reach the WebView. Warming the optimizer first
        # keeps that gap from opening.
        if (cd "$FRONTEND_DIR" && pnpm exec vite optimize >/dev/null 2>&1); then
            ok "Frontend" "vite dependencies pre-bundled"
        else
            warn "Frontend" "vite optimize failed; the first window load may race the dev server (right-click the window and Reload if it is blank)"
        fi
        # Signing. The dev bundle is com.wails.Isoform whatever
        # YANSU_APP_FLAVOR says (Info.dev.plist), so this applies to every
        # dev run. With the "Isoform Dev" identity (dev-signing-identity.sh
        # create) the post-build hook in wails.json (scripts/sign-dev-build.sh)
        # re-signs the dev bundle after every rebuild, so its designated
        # requirement, and the macOS privacy grants keyed to it, stay the
        # same across Go changes; the hook clears the grants once when they
        # were made for a differently signed build and logs to
        # ~/.isoform-agent/dev-signing/sign-dev-build.log. Without the
        # identity wails dev leaves an ad-hoc signature keyed to each build's
        # cdhash: the previous build's grants no longer apply (System Settings
        # shows switches that are on but ignored), so they are cleared and the
        # app asks again once (reset-dev-permissions.sh); every Go rebuild in
        # the session then breaks them again.
        # Only this script decides what the hook does: a value leaked from
        # the shell or .env.local must not reach it.
        unset YANSU_DEV_SIGNING YANSU_KEEP_PERMISSIONS
        DEV_APP="${WAILS_DIR}/build/bin/Isoform.app"
        if [ "$(uname -s)" != "Darwin" ]; then
            : # code signing and TCC are macOS only
        elif dev_identity="$(bash "${SCRIPT_DIR}/dev-signing-identity.sh" find)"; then
            export YANSU_DEV_SIGNING=1
            if [ "$KEEP_PERMISSIONS" = true ]; then
                export YANSU_KEEP_PERMISSIONS=1
            fi
            ok "Signing" "Isoform Dev identity $(printf '%s\n' "$dev_identity" | sed -n 1p); every rebuild is re-signed with it and the privacy grants survive"
        else
            if [ "$KEEP_PERMISSIONS" = true ]; then
                # Whatever gets granted to this ad-hoc run is keyed to its
                # cdhash, so the identity record is dropped and the next
                # identity-signed build clears once.
                bash "${SCRIPT_DIR}/reset-dev-permissions.sh" --forget >/dev/null 2>&1 || true
            # The previous dev bundle (if any) is passed so Launch Services
            # can resolve the bundle id even when it was never opened. A
            # plain clear also drops the identity record.
            elif bash "${SCRIPT_DIR}/reset-dev-permissions.sh" com.wails.Isoform "$DEV_APP" >/dev/null 2>&1; then
                ok "Privacy" "previous dev build's permission grants cleared (ad-hoc signature, keyed to the old binary); macOS will ask again"
            else
                warn "Privacy" "could not clear the previous build's permission grants; remove Isoform from Screen & System Audio Recording and Accessibility in System Settings and add it again"
            fi
            warn "Signing" "no usable Isoform Dev identity (the line above says why, if one exists); run wails-gui/scripts/dev-signing-identity.sh create so the grants survive rebuilds"
        fi
        info "Starting wails dev server..."
        echo "  (frontend HMR + Go backend — Ctrl+C to stop)"
        echo ""
        exec "$WAILS_BIN" "${dev_args[@]}"
        ;;
    build)
        info "Building production app..."
        bash "${SCRIPT_DIR}/build-wails.sh" --skip-cli
        ;;
esac
