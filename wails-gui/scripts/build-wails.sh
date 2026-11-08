#!/bin/bash
set -euo pipefail

# Build the Wails GUI app with the native agent runner (no subprocess bundles).
#
# Usage:
#   wails-gui/scripts/build-wails.sh                          # the Isoform release: bundled yansu CLI + wails build + embed
#   wails-gui/scripts/build-wails.sh --skip-cli               # skip yansu CLI bundle build
#   wails-gui/scripts/build-wails.sh --skip-whisper           # legacy no-op (whisper.cpp removed)
#   wails-gui/scripts/build-wails.sh --platform darwin/arm64  # cross-compile for a specific OS/arch
#   wails-gui/scripts/build-wails.sh --version 0.2.0          # inject version into wails.json before building
#   wails-gui/scripts/build-wails.sh --dmg                    # also create Isoform-<version>-darwin-<arch>.dmg with app icon (macOS only)
#   wails-gui/scripts/build-wails.sh --skip-spotlight         # skip the Quick Chat Spotlight helper build
#   wails-gui/scripts/build-wails.sh --skip-dumbpipe          # skip the pinned dumbpipe download
#   wails-gui/scripts/build-wails.sh --keep-permissions       # do not clear the previous Isoform build's macOS privacy grants (see reset-dev-permissions.sh)
#   wails-gui/scripts/build-wails.sh --flavor isoform         # development build of the same identity (see app_flavor.go)
#   wails-gui/scripts/build-wails.sh --flavor isoform --install # copy the signed app to ~/Applications/Isoform.app
#   wails-gui/scripts/build-wails.sh --flavor isoform --install --dry-run # print build/install plan without building (also on Linux)
#   ISOFORM_DESKTOP_REMOTE_ACCESS=0 wails-gui/scripts/build-wails.sh # opt out of the iroh access default
#
# Release and development builds are both Isoform.app, bundle id
# com.wails.Isoform, data in ~/.isoform-agent, so the person keeps one
# database. The release (no --flavor) is stamped -X main.buildChannel=release:
# it checks for updates, is signed with Developer ID or ad hoc, and skips every
# development step (Isoform Dev signing, privacy-grant clears, Launch Services
# cleanup, --install). --flavor isoform is the development build: no update
# checks, Isoform Dev signing when available. The legacy Yansu release identity
# is retired; --flavor yansu is refused.
#
# --install (development builds only) uses rsync to replace the installed bundle, preserving signatures
# and symlinks and removing stale files. Launch the installed copy for dogfood;
# login item registration prefers compatible ~/Applications/<Flavor>.app builds.
# Existing apps without this installer's marker are refused; move them aside
# before --install. The marker and login-command capability are signed metadata.
# --dry-run makes no changes and does not require the macOS build toolchain.
#
# Product telemetry (optional, build time only):
#   ISOFORM_DESKTOP_POSTHOG_KEY  PostHog project ingest key; never commit a value.
#   ISOFORM_DESKTOP_POSTHOG_HOST PostHog ingest origin, e.g. https://us.i.posthog.com.
#   (Deprecated fallback: YANSU_DESKTOP_POSTHOG_KEY / YANSU_DESKTOP_POSTHOG_HOST.)
#   ISOFORM_DESKTOP_PUSH_GATEWAY_URL  Optional HTTPS push gateway origin.
#   ISOFORM_DESKTOP_PUSH_GATEWAY_ENVIRONMENT  development or production.
#   ISOFORM_DESKTOP_PUSH_GATEWAY_PUBLIC_KEY  Base64url X25519 gateway key.
# Both default to empty; sending stays disabled unless both are supplied and
# diagnostic sharing is enabled. Release builds get them from
# scripts/release/ship.sh, which reads ~/.config/isoform-release/posthog.env;
# other builds may use the environment or gitignored wails-gui/.env.local.
# These apply to both release and --flavor isoform builds. The key is
# redacted from the wails build output.
# Runtime environment variables do not enable an unconfigured
# build. See docs/product-telemetry.md for the event and privacy contract.
# Before shipping a configured key, complete its IP-discard release checklist:
# docs/product-telemetry.md#release-gate-verify-ip-discard-before-adding-a-key
#
# Signing: APPLE_SIGNING_IDENTITY selects Developer ID signing plus
# notarization. Credentials are selected from a working Keychain profile, an
# App Store Connect API key, or APPLE_ID, APPLE_APP_SPECIFIC_PASSWORD and
# APPLE_TEAM_ID. See scripts/release/notary-credentials.sh for API key defaults.
# Without the signing identity, a --flavor isoform build is signed with the
# "Isoform Dev" identity when it exists (dev-signing-identity.sh create), so
# the bundle's designated requirement, and the macOS privacy grants keyed to
# it, stay the same across builds. Without that identity the build is ad-hoc
# signed, its requirement is the binary's cdhash, and the previous build's
# grants are cleared so the app asks once (reset-dev-permissions.sh).
#
# The Spotlight helper (wails-gui/quick-chat-spotlight/) is a separate
# binary spawned by Isoform at startup. It needs pnpm and Go (plus the
# wails CLI on Windows/Linux). Missing prerequisites or a failed helper
# build fail the app build. YANSU_ALLOW_NO_SPOTLIGHT=1 allows a warning
# and the in-app overlay fallback instead; --skip-spotlight explicitly
# skips the helper without attempting a build.
#
# Output filenames carry the version (when known) and a normalized arch:
#   build/bin/Isoform-<version>-darwin-<arch>.dmg
#   build/bin/Isoform-<version>-darwin-<arch>.app.tar.gz   (self-updater archive)
# The arch matches the values the updater's displayArch() emits (arm64, x64),
# so suffix matching against -darwin-<arch>.app.tar.gz works for both R2 and
# the GitHub fallback.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WAILS_DIR="$(dirname "$SCRIPT_DIR")"

# Load local dev/build env overrides if present. wails-gui/.env.local is
# the canonical place to put per-developer env (ISOFORM_DESKTOP_POSTHOG_KEY,
# YANSU_URL, etc.) that this script and start-wails-gui.sh both read.
# Gitignored. CI clones don't have the file so this is a no-op in
# yansu-desktop-releases — CI sets these via workflow `env:` blocks
# (see release.yml line 282-296).
if [ -f "${WAILS_DIR}/.env.local" ]; then
    # shellcheck disable=SC1090,SC1091
    set -a
    . "${WAILS_DIR}/.env.local"
    set +a
fi

# Public support address shown in Settings > Help (frontend
# src/lib/support-feedback.ts). The frontend build inherits it.
export VITE_SUPPORT_EMAIL="${VITE_SUPPORT_EMAIL:-bo@isoform.ai}"

CLI_BUNDLE="${WAILS_DIR}/yansu-cli-bundle"
# wails names the bundle after wails.json "name" (Isoform).
APP_NAME="Isoform.app"
APP_PATH="${WAILS_DIR}/build/bin/${APP_NAME}"
# The inner executable (Contents/MacOS/<EXE_BASENAME>) is wails' outputfilename,
# an internal name kept from Yansu. Keep these decoupled so signing targets
# the real executable.
EXE_BASENAME="Yansu"

BUN_BUNDLE="${WAILS_DIR}/bun-bundle"
FFMPEG_BUNDLE="${WAILS_DIR}/ffmpeg-bundle"
OPENCLI_BUNDLE="${WAILS_DIR}/opencli-bundle"
MAGPIE_BUNDLE="${WAILS_DIR}/magpie-bundle"
DUMBPIPE_BUNDLE="${WAILS_DIR}/dumbpipe-bundle"
RELAY_BUNDLE="${WAILS_DIR}/relay-bundle"
GIT_BUNDLE="${WAILS_DIR}/git-bundle"
PADDLEOCR_BUNDLE="${WAILS_DIR}/paddleocr-bundle"
SPOTLIGHT_DIR="${WAILS_DIR}/quick-chat-spotlight"
CUA_DRIVER_BUNDLE="${WAILS_DIR}/cua-driver-bundle"
OCR_HELPER_NAME="yansu-ocr-helper"
PII_HELPER_NAME="yansu-pii-helper"

SKIP_CLI=false
SKIP_WHISPER=false # retained for backward-compat with existing CI invocations; no-op since sherpa-onnx replaced whisper.cpp
SKIP_BUN=false
SKIP_FFMPEG=false
SKIP_MAGPIE=false
SKIP_DUMBPIPE=false
SKIP_GIT=false
SKIP_PADDLEOCR=false
SKIP_SPOTLIGHT=false
SPOTLIGHT_BUILT=false
SKIP_CUA_DRIVER=false
PLATFORM=""
VERSION=""
CREATE_DMG=false
EXTRA_LDFLAGS=""
FLAVOR=""
KEEP_PERMISSIONS=false
INSTALL_APP=false
DRY_RUN=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --skip-cli)
            SKIP_CLI=true
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
        --skip-ffmpeg)
            SKIP_FFMPEG=true
            shift
            ;;
        --skip-magpie)
            SKIP_MAGPIE=true
            shift
            ;;
        --skip-dumbpipe)
            SKIP_DUMBPIPE=true
            shift
            ;;
        --skip-paddleocr)
            SKIP_PADDLEOCR=true
            shift
            ;;
        --skip-cua-driver)
            SKIP_CUA_DRIVER=true
            shift
            ;;
        --skip-git)
            SKIP_GIT=true
            shift
            ;;
        --skip-spotlight)
            SKIP_SPOTLIGHT=true
            shift
            ;;
        --platform)
            PLATFORM="${2:?'--platform requires an argument (e.g. darwin/arm64)'}"
            shift 2
            ;;
        --version)
            VERSION="${2:?'--version requires a version string (e.g. 0.2.0)'}"
            shift 2
            ;;
        --dmg)
            CREATE_DMG=true
            shift
            ;;
        --ldflags)
            EXTRA_LDFLAGS="${2:?'--ldflags requires a value'}"
            shift 2
            ;;
        --keep-permissions)
            # Leave the previous Isoform build's macOS privacy grants in place.
            # By default an ad-hoc signed isoform build clears them, because
            # the new signature no longer matches them (reset-dev-permissions.sh).
            KEEP_PERMISSIONS=true
            shift
            ;;
        --install)
            INSTALL_APP=true
            shift
            ;;
        --dry-run)
            DRY_RUN=true
            shift
            ;;
        --flavor)
            # Build flavor (wails-gui/app_flavor.go). "isoform" is the
            # development build of the Isoform identity; without --flavor
            # the script builds the Isoform release.
            FLAVOR="$(printf '%s' "${2:?'--flavor requires isoform'}" | tr '[:upper:]' '[:lower:]')"
            case "$FLAVOR" in
                isoform) ;;
                yansu)
                    echo "[build-wails] --flavor yansu is retired: the release is Isoform now; omit --flavor for the release or pass --flavor isoform for a development build" >&2
                    exit 2
                    ;;
                *)
                    echo "[build-wails] --flavor must be isoform (or omitted for the release), got '$2'" >&2
                    exit 2
                    ;;
            esac
            shift 2
            ;;
        *)
            echo "[build-wails] Warning: unknown option '$1'" >&2
            shift
            ;;
    esac
done

detect_host_os() {
    local os
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    case "$os" in
        darwin) echo "darwin" ;;
        linux) echo "linux" ;;
        mingw*|msys*|cygwin*|windows*) echo "windows" ;;
        *)
            echo "[build-wails] Unsupported host OS: $os" >&2
            exit 1
            ;;
    esac
}

detect_host_arch() {
    local arch
    arch="$(uname -m)"
    case "$arch" in
        arm64|aarch64) echo "arm64" ;;
        x86_64|amd64) echo "amd64" ;;
        *)
            echo "[build-wails] Unsupported host architecture: $arch" >&2
            exit 1
            ;;
    esac
}

configure_go_proxy() {
    local default_proxy configured_proxy
    default_proxy="${YANSU_GOPROXY:-https://proxy.golang.org,direct}"
    configured_proxy="${GOPROXY:-}"

    case "$configured_proxy" in
        ""|*goproxy.io*)
            export GOPROXY="$default_proxy"
            ;;
        *)
            if [ -n "${YANSU_GOPROXY:-}" ]; then
                export GOPROXY="$YANSU_GOPROXY"
            fi
            ;;
    esac
}

# Keep validation and dry-run ahead of downloads, version edits and builds.
if [ "$INSTALL_APP" = true ]; then
    case "${PLATFORM:-darwin}" in
        darwin|darwin/*) ;;
        *) echo "[build-wails] --install requires a macOS app bundle" >&2; exit 2 ;;
    esac
    if [ "$DRY_RUN" != true ] && [ "$(uname -s)" != "Darwin" ]; then
        echo "[build-wails] --install requires macOS; use --dry-run to inspect the plan on this host" >&2
        exit 2
    fi
fi

INSTALL_MARKER_TOKEN="isoform-build-install-v1"
check_install_destination() {
    local destination marker expected
    destination="$1"
    marker="${destination}/Contents/Resources/.isoform-build-install"
    expected="${INSTALL_MARKER_TOKEN}:${destination##*/}"
    if [ -L "$destination" ]; then
        echo "[build-wails] Refusing to replace a symlink at ${destination}; move it aside and install again" >&2
        return 1
    fi
    if [ -e "$destination" ]; then
        if [ ! -f "$marker" ] || [ -L "$marker" ] || [ "$(cat "$marker")" != "$expected" ]; then
            echo "[build-wails] Refusing to replace unmanaged app at ${destination}; move it aside and rerun --install" >&2
            return 1
        fi
    fi
}

# The release shares the development build's identity and data folder, so
# installing it would replace the development app the person runs daily.
if [ "$INSTALL_APP" = true ] && [ "$FLAVOR" != "isoform" ]; then
    echo "[build-wails] --install is for development builds (--flavor isoform); the release is distributed through its DMG" >&2
    exit 2
fi

# Refuse before doing an expensive build, then check again just before copying.
if [ "$INSTALL_APP" = true ]; then
    check_install_destination "${HOME:?HOME must be set}/Applications/${APP_NAME}"
fi

install_built_app() {
    local destination
    destination="${HOME:?HOME must be set}/Applications/${APP_NAME}"
    check_install_destination "$destination"
    if [ "$DRY_RUN" = true ]; then
        printf '[build-wails] dry run: mkdir -p %q\n' "${HOME}/Applications"
        printf '[build-wails] dry run: /usr/bin/rsync -aE --checksum --delete %q %q\n' "${APP_PATH}/" "${destination}/"
        printf '[build-wails] dry run: codesign --verify --deep --strict %q\n' "$destination"
        return
    fi
    mkdir -p "${HOME}/Applications"
    # macOS system rsync -E preserves extended attributes/resource forks;
    # -a preserves symlinks/modes, --delete removes old signed resources, and
    # --checksum also replaces files whose sizes and mtimes happen to match.
    /usr/bin/rsync -aE --checksum --delete "${APP_PATH}/" "${destination}/"
    codesign --verify --deep --strict "$destination"
    echo "[build-wails] Installed ${destination} (signature preserved)"
}

if [ "$DRY_RUN" = true ]; then
    printf '[build-wails] dry run: build and sign %q\n' "$APP_PATH"
    if [ "$INSTALL_APP" = true ]; then
        install_built_app
    fi
    exit 0
fi

source "${SCRIPT_DIR}/bundle-architecture.sh"
configure_go_proxy

git_target_platform() {
    local target_os target_arch
    target_os="${GOOS:-$(detect_host_os)}"
    target_arch="${GOARCH:-$(detect_host_arch)}"
    echo "${target_os}/${target_arch}"
}

git_bundle_matches_target() {
    local target_platform metadata_file
    target_platform="$1"
    metadata_file="${GIT_BUNDLE}/.target-platform"
    [ -f "$metadata_file" ] && [ "$(cat "$metadata_file")" = "$target_platform" ]
}

is_macho_binary() {
    local file_path
    file_path="$1"
    file -b "$file_path" 2>/dev/null | grep -Eq 'Mach-O|universal binary'
}

# The signing step sets CODESIGN_IDENTITY (the identity codesign signs with),
# CODESIGN_OPTIONS (its per-identity flags: timestamp and hardened runtime for
# Developer ID, neither for the development identity) and CODESIGN_LABEL (for
# log lines) before calling the sign_* functions below.
sign_macos_binary() {
    local file_path relative_path
    file_path="$1"
    relative_path="${file_path#${APP_PATH}/}"

    echo "[build-wails] Signing nested code: ${relative_path}"
    if [[ "$relative_path" == */yansu-cli-bundle/bin/yansu ]]; then
        # This CLI has a narrow agent-sandbox credential exception. Its
        # hardened runtime must survive development-identity re-signing.
        codesign --force "${CODESIGN_OPTIONS[@]}" --options runtime --sign "$CODESIGN_IDENTITY" "$file_path"
    elif [ "$(basename "$file_path")" = "$PII_HELPER_NAME" ]; then
        codesign --force "${CODESIGN_OPTIONS[@]}" --entitlements "${WAILS_DIR}/scripts/pii-helper.entitlements.plist" --sign "$CODESIGN_IDENTITY" "$file_path"
    else
        codesign --force "${CODESIGN_OPTIONS[@]}" --sign "$CODESIGN_IDENTITY" "$file_path"
    fi
    codesign --verify --strict --verbose=2 "$file_path"
}

# designated_requirement <bundle> prints the bundle's designated requirement,
# the thing macOS keys privacy grants to, or nothing when it is unsigned.
# codesign prefixes an ad-hoc (implicit) requirement with "# ".
designated_requirement() {
    codesign -d -r- "$1" 2>&1 | sed -n 's/^\(# \)\{0,1\}designated => //p' || true
}

# sherpa_module_lib_dir <os> <arch>
# Locate the sherpa-onnx-go-* module's lib/ dir for a given target.
# Echoes the absolute path on success; returns 1 if not found.
sherpa_module_lib_dir() {
    local target_os target_arch module subdir base
    target_os="$1"
    target_arch="$2"

    case "$target_os" in
        darwin)
            module="github.com/k2-fsa/sherpa-onnx-go-macos"
            case "$target_arch" in
                arm64|aarch64) subdir="aarch64-apple-darwin" ;;
                amd64|x86_64)  subdir="x86_64-apple-darwin" ;;
                *) return 1 ;;
            esac
            ;;
        windows)
            module="github.com/k2-fsa/sherpa-onnx-go-windows"
            case "$target_arch" in
                amd64|x86_64) subdir="x86_64-pc-windows-gnu" ;;
                386|i686)     subdir="i686-pc-windows-gnu" ;;
                *) return 1 ;;
            esac
            ;;
        linux)
            module="github.com/k2-fsa/sherpa-onnx-go-linux"
            case "$target_arch" in
                arm64|aarch64)   subdir="aarch64-unknown-linux-gnu" ;;
                amd64|x86_64)    subdir="x86_64-unknown-linux-gnu" ;;
                arm|armv7|armhf) subdir="arm-unknown-linux-gnueabihf" ;;
                *) return 1 ;;
            esac
            ;;
        *)
            return 1
            ;;
    esac

    base="$(cd "$WAILS_DIR" && go list -m -f '{{.Dir}}' "$module" 2>/dev/null)"
    # Strip a trailing CR if go.exe emitted Windows-style line endings (happens
    # under MSYS bash command substitution on the GitHub windows-latest runner).
    base="${base%$'\r'}"
    if [ -z "$base" ]; then
        # `go list -m -f '{{.Dir}}'` returns empty (silently, exit 0) when the
        # module isn't yet in the local cache. This happens on fresh CI
        # checkouts before `wails build` has had a chance to populate the
        # cache via its own `go mod download`. Download explicitly and retry.
        echo "[build-wails] sherpa_module_lib_dir: $module not in module cache, fetching..." >&2
        (cd "$WAILS_DIR" && go mod download "$module" >/dev/null 2>&1) || true
        base="$(cd "$WAILS_DIR" && go list -m -f '{{.Dir}}' "$module" 2>/dev/null)"
        base="${base%$'\r'}"
    fi
    if [ -z "$base" ]; then
        echo "[build-wails] sherpa_module_lib_dir: 'go list -m $module' returned empty after fetch (module not in module graph?)" >&2
        return 1
    fi
    # On a Windows host, `go list -m` returns a path like
    # `C:\Users\runneradmin\go\pkg\mod\...`. MSYS bash's `[ -d ]` test is
    # unreliable on Windows-style paths with backslashes and a drive-letter
    # colon. Convert to MSYS form (`/c/Users/...`) so subsequent ops work
    # uniformly across hosts. cygpath is absent on macOS / Linux where the
    # path is already unix-style and no conversion is needed.
    if command -v cygpath >/dev/null 2>&1; then
        base="$(cygpath -u "$base")"
    fi
    local dir="${base}/lib/${subdir}"
    if [ ! -d "$dir" ]; then
        echo "[build-wails] sherpa_module_lib_dir: expected lib dir does not exist: $dir" >&2
        return 1
    fi
    echo "$dir"
}

# bundle_sherpa_dylibs_macos <app_bundle_arch>
# Copies sherpa-onnx + onnxruntime dylibs into Yansu.app/Contents/Frameworks/,
# rewrites RPATH entries on the main executable so they point at the bundled
# Frameworks dir, and updates dylib install_names so the loader resolves them
# via @rpath. Should be called before codesign so signatures cover the libs.
bundle_sherpa_dylibs_macos() {
    local lib_src onnx_src frameworks main_exe
    lib_src="$(sherpa_module_lib_dir "darwin" "$EFFECTIVE_TARGET_ARCH")" || {
        echo "[build-wails] Warning: could not locate sherpa-onnx dylibs for darwin/${EFFECTIVE_TARGET_ARCH}, skipping bundling" >&2
        return 0
    }
    frameworks="${APP_PATH}/Contents/Frameworks"
    main_exe="${APP_PATH}/Contents/MacOS/${EXE_BASENAME}"
    onnx_src="$lib_src"
    if [ "$EFFECTIVE_TARGET_ARCH" = amd64 ]; then
        onnx_src="${WAILS_DIR}/onnxruntime-bundle/lib"
    fi

    mkdir -p "$frameworks"
    echo "[build-wails] Bundling sherpa-onnx dylibs from ${lib_src}"
    for library in libsherpa-onnx-c-api.dylib libsherpa-onnx-cxx-api.dylib libonnxruntime.dylib libonnxruntime.1.24.4.dylib; do
        if [ -f "${lib_src}/${library}" ] && ! bundle_macho_matches "${lib_src}/${library}" "$EFFECTIVE_TARGET_ARCH"; then
            echo "[build-wails] ERROR: ${library} does not contain ${EFFECTIVE_TARGET_ARCH}" >&2
            return 1
        fi
    done
    cp -f "${lib_src}/libsherpa-onnx-c-api.dylib"   "$frameworks/"
    cp -f "${lib_src}/libsherpa-onnx-cxx-api.dylib" "$frameworks/"
    cp -f "${onnx_src}/libonnxruntime.dylib"         "$frameworks/"
    if [ -f "${onnx_src}/libonnxruntime.1.24.4.dylib" ]; then
        cp -f "${onnx_src}/libonnxruntime.1.24.4.dylib" "$frameworks/"
    fi

    chmod 0755 "$frameworks"/*.dylib

    # Make each dylib report itself via @rpath instead of an absolute build path.
    install_name_tool -id "@rpath/libsherpa-onnx-c-api.dylib"   "$frameworks/libsherpa-onnx-c-api.dylib"
    install_name_tool -id "@rpath/libsherpa-onnx-cxx-api.dylib" "$frameworks/libsherpa-onnx-cxx-api.dylib"
    install_name_tool -id "@rpath/libonnxruntime.dylib"         "$frameworks/libonnxruntime.dylib"
    if [ -f "$frameworks/libonnxruntime.1.24.4.dylib" ]; then
        install_name_tool -id "@rpath/libonnxruntime.1.24.4.dylib" "$frameworks/libonnxruntime.1.24.4.dylib"
    fi

    # Cross-link dependencies between the sherpa libs.
    install_name_tool -change "@rpath/libonnxruntime.1.24.4.dylib" "@rpath/libonnxruntime.1.24.4.dylib" "$frameworks/libsherpa-onnx-c-api.dylib" 2>/dev/null || true
    install_name_tool -change "@rpath/libonnxruntime.dylib"         "@rpath/libonnxruntime.dylib"        "$frameworks/libsherpa-onnx-c-api.dylib" 2>/dev/null || true

    # Strip any RPATH entries that point at the developer's Go module cache and
    # add a single @executable_path/../Frameworks entry. install_name_tool's
    # -delete_rpath errors when the path is absent, so swallow non-zero exits.
    while IFS= read -r rpath; do
        case "$rpath" in
            @executable_path/../Frameworks) ;;
            *) install_name_tool -delete_rpath "$rpath" "$main_exe" 2>/dev/null || true ;;
        esac
    done < <(otool -l "$main_exe" | awk '/LC_RPATH/{getline; getline; print $2}')
    if ! otool -l "$main_exe" | grep -q '@executable_path/../Frameworks'; then
        install_name_tool -add_rpath "@executable_path/../Frameworks" "$main_exe"
    fi
}

# bundle_sherpa_dlls_windows <bin_dir>
# Copies sherpa-onnx + onnxruntime DLLs next to Yansu.exe so the Windows loader
# finds them via the executable directory.
bundle_sherpa_dlls_windows() {
    local bin_dir lib_src
    bin_dir="$1"
    lib_src="$(sherpa_module_lib_dir "windows" "$EFFECTIVE_TARGET_ARCH")" || {
        echo "[build-wails] Warning: could not locate sherpa-onnx DLLs for windows/${EFFECTIVE_TARGET_ARCH}, skipping bundling" >&2
        return 0
    }
    echo "[build-wails] Bundling sherpa-onnx DLLs from ${lib_src}"
    cp -f "${lib_src}/sherpa-onnx-c-api.dll"   "$bin_dir/"
    cp -f "${lib_src}/sherpa-onnx-cxx-api.dll" "$bin_dir/"
    cp -f "${lib_src}/onnxruntime.dll"         "$bin_dir/"
}

# bundle_sherpa_libs_linux <bin_dir>
# Copies sherpa-onnx + onnxruntime .so files next to the Yansu binary and
# rewrites the ELF RUNPATH so the dynamic loader resolves them via $ORIGIN
# (the executable's own directory).
#
# Why this is needed: the sherpa-onnx-go-linux cgo build flags bake an absolute
# `-Wl,-rpath,${SRCDIR}/lib/<triple>` into the binary at link time, pointing at
# the *build host's* Go module cache (e.g. /home/runner/go/pkg/mod/...). That
# path doesn't exist on an end user's machine, so the loader can't find the
# libraries and the binary dies before main() with:
#   ./Yansu: error while loading shared libraries: libsherpa-onnx-c-api.so:
#   cannot open shared object file: No such file or directory
# This mirrors bundle_sherpa_dylibs_macos (Frameworks + @rpath) and
# bundle_sherpa_dlls_windows (DLLs next to the exe).
bundle_sherpa_libs_linux() {
    local bin_dir lib_src exe
    bin_dir="$1"
    exe="${bin_dir}/Yansu"
    lib_src="$(sherpa_module_lib_dir "linux" "$EFFECTIVE_TARGET_ARCH")" || {
        echo "[build-wails] Warning: could not locate sherpa-onnx .so libs for linux/${EFFECTIVE_TARGET_ARCH}, skipping bundling" >&2
        return 0
    }
    echo "[build-wails] Bundling sherpa-onnx .so libs from ${lib_src}"
    cp -f "${lib_src}/libsherpa-onnx-c-api.so"   "$bin_dir/"
    cp -f "${lib_src}/libsherpa-onnx-cxx-api.so" "$bin_dir/"
    cp -f "${lib_src}/libonnxruntime.so"         "$bin_dir/"
    chmod 0755 "$bin_dir"/*.so

    if command -v patchelf >/dev/null 2>&1; then
        # Point the executable at its own directory so it finds
        # libsherpa-onnx-c-api.so next to it.
        patchelf --set-rpath '$ORIGIN' "$exe"
        # RUNPATH is NOT transitive: when the loader resolves
        # libsherpa-onnx-c-api.so's own dependency on libonnxruntime.so it
        # consults the .so's RUNPATH, not the executable's. So each sherpa lib
        # needs its own $ORIGIN entry to find its siblings in the same dir.
        patchelf --set-rpath '$ORIGIN' "$bin_dir/libsherpa-onnx-c-api.so"
        patchelf --set-rpath '$ORIGIN' "$bin_dir/libsherpa-onnx-cxx-api.so"
        echo "[build-wails] Patched ELF RUNPATH to \$ORIGIN on Yansu + sherpa libs"
    else
        echo "[build-wails] WARNING: patchelf not found — the Linux binary still carries the build-host rpath and will fail with 'libsherpa-onnx-c-api.so: cannot open shared object file' on end-user machines. Install patchelf (apt-get install patchelf / pacman -S patchelf) before building Linux releases." >&2
    fi
}

sign_macos_embedded_code() {
    local file_path
    local cua_skip_prefix="${APP_PATH}/Contents/Resources/cua-driver-bundle/CuaDriver.app"
    local spotlight_skip_prefix="${APP_PATH}/Contents/Resources/quick-chat-spotlight.app"

    while IFS= read -r -d '' file_path; do
        if ! is_macho_binary "$file_path"; then
            continue
        fi

        if [ "$file_path" = "${APP_PATH}/Contents/MacOS/${EXE_BASENAME}" ]; then
            continue
        fi

        # cua-driver is signed separately by sign_cua_driver_nested with
        # its own entitlements (com.apple.security.automation.apple-events).
        # Letting the generic walk re-sign it would clobber those.
        case "$file_path" in
            "$cua_skip_prefix"/*) continue ;;
        esac

        # quick-chat-spotlight is packaged as its own background .app
        # bundle on macOS so LaunchServices reads its LSUIElement plist
        # instead of treating it as a second Yansu.app instance.
        case "$file_path" in
            "$spotlight_skip_prefix"/*) continue ;;
        esac

        sign_macos_binary "$file_path"
    # This recursive Contents walk signs every nested Mach-O, including the
    # dumbpipe and relay-bundle binaries in Resources.
    done < <(find "$APP_PATH/Contents" -type f -print0)
}

# sign_cua_driver_nested re-signs the embedded CuaDriver.app with the
# build's signing identity (CODESIGN_IDENTITY), preserving the upstream
# entitlements (com.apple.security.automation.apple-events) that
# AppleEvent-driven tool calls depend on. Must run before
# sign_macos_embedded_code so the generic walk sees an already-signed
# binary and skips it.
#
# Developer ID builds pass --options runtime (CODESIGN_OPTIONS) because the
# upstream binary was built against hardened runtime; signing without it
# would downgrade the protection and trip notarization rejections.
sign_cua_driver_nested() {
    local cua_app cua_binary entitlements
    cua_app="${APP_PATH}/Contents/Resources/cua-driver-bundle/CuaDriver.app"
    cua_binary="${cua_app}/Contents/MacOS/cua-driver"
    entitlements="${cua_app}/Contents/Resources/cua-driver.entitlements"

    if [ ! -d "$cua_app" ]; then
        return 0
    fi
    if [ ! -f "$entitlements" ]; then
        echo "[build-wails] Warning: cua-driver entitlements not found at ${entitlements}; signing without entitlements." >&2
        entitlements=""
    fi

    echo "[build-wails] Re-signing nested CuaDriver.app with ${CODESIGN_LABEL}"

    # Strip the ad-hoc signature the download script applied for dev runs;
    # codesign --force is supposed to handle this but ad-hoc → Developer ID
    # transitions sometimes leave stale CodeResources hashes that confuse
    # `codesign --verify --deep --strict`.
    codesign --remove-signature "$cua_binary" 2>/dev/null || true
    codesign --remove-signature "$cua_app" 2>/dev/null || true
    rm -rf "${cua_app}/Contents/_CodeSignature"

    local sign_args=(--force "${CODESIGN_OPTIONS[@]}" --sign "$CODESIGN_IDENTITY")
    if [ -n "$entitlements" ]; then
        sign_args+=(--entitlements "$entitlements")
    fi

    # Inside-out: binary first, then the bundle.
    codesign "${sign_args[@]}" "$cua_binary"
    codesign "${sign_args[@]}" "$cua_app"
    codesign --verify --deep --strict --verbose=2 "$cua_app"
}

sign_spotlight_nested() {
    local spotlight_app spotlight_binary
    spotlight_app="${APP_PATH}/Contents/Resources/quick-chat-spotlight.app"
    spotlight_binary="${spotlight_app}/Contents/MacOS/quick-chat-spotlight"

    if [ ! -d "$spotlight_app" ]; then
        return 0
    fi
    if [ ! -f "$spotlight_binary" ]; then
        echo "[build-wails] Warning: quick-chat-spotlight binary not found at ${spotlight_binary}; skipping nested app signing." >&2
        return 0
    fi

    echo "[build-wails] Re-signing nested quick-chat-spotlight.app with ${CODESIGN_LABEL}"

    codesign --remove-signature "$spotlight_binary" 2>/dev/null || true
    codesign --remove-signature "$spotlight_app" 2>/dev/null || true
    rm -rf "${spotlight_app}/Contents/_CodeSignature"

    # The spotlight WebView uses getUserMedia for voice input, so the
    # hardened-runtime bundle needs the audio-input entitlement (the main
    # Yansu.app's entitlements don't propagate to this nested helper).
    local spotlight_entitlements="${WAILS_DIR}/scripts/spotlight.entitlements.plist"
    local sign_args=(--force "${CODESIGN_OPTIONS[@]}" --sign "$CODESIGN_IDENTITY")
    if [ -f "$spotlight_entitlements" ]; then
        sign_args+=(--entitlements "$spotlight_entitlements")
    else
        echo "[build-wails] Warning: spotlight entitlements not found at ${spotlight_entitlements}; voice input may be blocked." >&2
    fi

    codesign "${sign_args[@]}" "$spotlight_binary"
    codesign "${sign_args[@]}" "$spotlight_app"
    codesign --verify --deep --strict --verbose=2 "$spotlight_app"
}

mark_spotlight_helper_lsui_element() {
    local helper_plist
    helper_plist="$1"

    if [ ! -f "$helper_plist" ]; then
        return 0
    fi

    /usr/libexec/PlistBuddy -c "Add :LSUIElement bool true" "$helper_plist" 2>/dev/null ||
        /usr/libexec/PlistBuddy -c "Set :LSUIElement true" "$helper_plist" 2>/dev/null ||
        echo "[build-wails] Warning: failed to mark quick-chat-spotlight as LSUIElement" >&2
}

stamp_macos_bundle_versions() {
    local plist="$1" short="${VERSION%%-*}"
    /usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${short}" "$plist"
    /usr/libexec/PlistBuddy -c "Set :CFBundleVersion ${BUNDLE_BUILD_NUMBER}" "$plist"
    /usr/libexec/PlistBuddy -c "Add :IsoformReleaseVersion string ${VERSION}" "$plist" 2>/dev/null \
        || /usr/libexec/PlistBuddy -c "Set :IsoformReleaseVersion ${VERSION}" "$plist"
}

# write_spotlight_info_plist writes the Info.plist for the native macOS
# spotlight .app (no Wails generates one). CFBundleIdentifier is required
# for code signing + notarization; LSUIElement keeps it out of the Dock.
write_spotlight_info_plist() {
    local plist_path version
    plist_path="$1"
    version="${VERSION:-0.1.0}"
    version="${version%%-*}"
    mkdir -p "$(dirname "$plist_path")"
    cat >"$plist_path" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleExecutable</key><string>quick-chat-spotlight</string>
    <key>CFBundleIdentifier</key><string>com.yansu.quick-chat-spotlight</string>
    <key>CFBundleName</key><string>Isoform Spotlight</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
    <key>CFBundleShortVersionString</key><string>${version}</string>
    <key>CFBundleVersion</key><string>${BUNDLE_BUILD_NUMBER}</string>
    <key>LSUIElement</key><true/>
    <key>LSMinimumSystemVersion</key><string>14.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>NSMicrophoneUsageDescription</key><string>Isoform uses the microphone for voice typing.</string>
</dict>
</plist>
EOF
}

extract_notary_value() {
    local json_file key
    json_file="$1"
    key="$2"
    sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" "$json_file" | head -1
}

# shellcheck source=../../scripts/release/notary-credentials.sh
. "${WAILS_DIR}/../scripts/release/notary-credentials.sh"

# notarize_with_retry <artifact_path> <label>
# Submits <artifact_path> to Apple's notary service and waits for the result,
# tolerating transient network failures.
#
# Why this exists: `notarytool submit --wait` couples the upload and the
# status-polling into one command. The polling phase makes repeated HTTPS calls
# to appstoreconnect.apple.com, and a single timed-out request (NSURLError
# -1001 / ETIMEDOUT 60) aborts the whole command with exit 1 — even though the
# submission already succeeded and Apple is still processing it. On GitHub's
# macOS runners these transient timeouts are common and previously failed the
# entire release build.
#
# Fix: submit once (--no-wait) to obtain a submission id, retrying only the
# submit on network errors, then poll with `notarytool wait`, retrying the wait
# (NOT re-uploading) on network errors. Echoes the submission id on the way so
# callers can fetch logs. Returns 0 only when the final status is "Accepted".
notarize_with_retry() {
    local artifact label notary_id status output attempt max_attempts
    artifact="$1"
    label="$2"
    max_attempts=5
    output=$(mktemp -t yansu-notary.XXXXXX).json

    # Phase 1: submit (no wait) and obtain a submission id. Retry the upload on
    # transient failures since we have no id to resume from yet.
    notary_id=""
    for attempt in $(seq 1 "$max_attempts"); do
        if notarytool_with_credentials submit "$artifact" \
            --no-wait \
            --output-format json > "$output"; then
            notary_id="$(extract_notary_value "$output" id)"
            [ -n "$notary_id" ] && break
        fi
        echo "[build-wails] ${label}: notarytool submit failed (attempt ${attempt}/${max_attempts}), retrying in 30s..." >&2
        sleep 30
    done

    if [ -z "$notary_id" ]; then
        echo "[build-wails] ${label}: failed to obtain a notarization submission id after ${max_attempts} attempts" >&2
        rm -f "$output"
        return 1
    fi
    echo "[build-wails] ${label}: submitted, submission id ${notary_id}"
    NOTARY_ID="$notary_id"

    # Phase 2: poll for the result. Retry only the wait on transient network
    # errors; the upload is already done so we never re-transmit the artifact.
    status=""
    for attempt in $(seq 1 "$max_attempts"); do
        if notarytool_with_credentials wait "$notary_id" \
            --timeout 1200 \
            --output-format json > "$output"; then
            status="$(extract_notary_value "$output" status)"
            break
        fi
        echo "[build-wails] ${label}: notarytool wait failed (network?), attempt ${attempt}/${max_attempts}, retrying in 30s..." >&2
        sleep 30
    done

    rm -f "$output"

    if [ "$status" = "Accepted" ]; then
        return 0
    fi
    echo "[build-wails] ${label}: notarization failed with status: ${status:-unknown}" >&2
    return 1
}

# bun_bundle_arch_ok <bun_binary>
# Returns 0 (true) when the binary's arch matches the expected target.
# Prints a warning and returns 1 on mismatch; returns 0 silently on skip
# (Windows targets or unrecognised BUN_TARGET).
bun_bundle_arch_ok() {
    local bun_bin expected_arch actual_arch target
    bun_bin="$1"
    target="${BUN_TARGET:-}"

    # Determine expected arch substring from BUN_TARGET, or fall back to uname -m.
    if [ -n "$target" ]; then
        case "$target" in
            *-windows-*) return 0 ;;   # skip check; file(1) output is less uniform
            *-aarch64)   expected_arch="arm64" ;;
            *-x64)       expected_arch="x86_64" ;;
            *)
                echo "[build-wails] Warning: unrecognised BUN_TARGET '${target}', skipping arch check" >&2
                return 0
                ;;
        esac
    else
        expected_arch="$(uname -m)"
    fi

    actual_arch="$(file -b "$bun_bin" 2>/dev/null)"

    # Permissive matching per platform conventions (see spec).
    case "$expected_arch" in
        arm64|aarch64)
            echo "$actual_arch" | grep -qE 'arm64|aarch64' && return 0 ;;
        x86_64|amd64)
            echo "$actual_arch" | grep -qE 'x86-64|x86_64' && return 0 ;;
    esac

    echo "[build-wails] WARNING: bun-bundle arch mismatch — expected ${expected_arch}, got: ${actual_arch}" >&2
    echo "[build-wails] Deleting stale bundle so a correct one will be downloaded." >&2
    rm -rf "${BUN_BUNDLE}"
    return 1
}

# ── Derive cross-compilation env from --platform ────────────────────
# Maps a wails platform string (e.g. "darwin/arm64") to the environment
# variables needed by sub-scripts (BUN_TARGET, GOARCH).
if [ -n "$PLATFORM" ]; then
    TARGET_OS="${PLATFORM%%/*}"
    TARGET_ARCH="${PLATFORM##*/}"

    export GOARCH="$TARGET_ARCH"
    export GOOS="$TARGET_OS"

    # Map to bun release target names.
    case "${TARGET_ARCH}-${TARGET_OS}" in
        arm64-darwin)  export BUN_TARGET="bun-darwin-aarch64" ;;
        amd64-darwin)  export BUN_TARGET="bun-darwin-x64"     ;;
        arm64-linux)   export BUN_TARGET="bun-linux-aarch64"  ;;
        amd64-linux)   export BUN_TARGET="bun-linux-x64"      ;;
        amd64-windows) export BUN_TARGET="bun-windows-x64"    ;;
        *)             echo "[build-wails] Warning: no bun mapping for ${PLATFORM}" >&2 ;;
    esac

    # Map to ffmpeg-static release target names (eugeneware/ffmpeg-static).
    case "${TARGET_ARCH}-${TARGET_OS}" in
        arm64-darwin)  export FFMPEG_TARGET="darwin-arm64" ;;
        amd64-darwin)  export FFMPEG_TARGET="darwin-x64"   ;;
        arm64-linux)   export FFMPEG_TARGET="linux-arm64"  ;;
        amd64-linux)   export FFMPEG_TARGET="linux-x64"    ;;
        amd64-windows) export FFMPEG_TARGET="win32-x64"    ;;
        *)             echo "[build-wails] Warning: no ffmpeg mapping for ${PLATFORM}" >&2 ;;
    esac

    # sherpa-onnx-go ships cgo bindings; cross-compiling Windows targets from a
    # non-Windows host requires CGO_ENABLED=1 and a mingw-w64 toolchain.
    # Without CGO_ENABLED Go silently strips the cgo files, leading to
    # "build constraints exclude all Go files" errors from
    # sherpa-onnx-go-windows during the wails compile step.
    if [ "$TARGET_OS" = "windows" ]; then
        export CGO_ENABLED=1
        case "$TARGET_ARCH" in
            amd64)
                export CC="${CC:-x86_64-w64-mingw32-gcc}"
                export CXX="${CXX:-x86_64-w64-mingw32-g++}"
                ;;
            386)
                export CC="${CC:-i686-w64-mingw32-gcc}"
                export CXX="${CXX:-i686-w64-mingw32-g++}"
                ;;
        esac
        if ! command -v "$CC" >/dev/null 2>&1; then
            echo "[build-wails] ERROR: mingw-w64 toolchain '$CC' not found in PATH." >&2
            echo "[build-wails]        On macOS: brew install mingw-w64" >&2
            echo "[build-wails]        On Linux: apt-get install gcc-mingw-w64" >&2
            exit 1
        fi
    fi

    echo "[build-wails] Cross-compiling for ${PLATFORM} (GOARCH=${TARGET_ARCH}, BUN_TARGET=${BUN_TARGET:-auto}, FFMPEG_TARGET=${FFMPEG_TARGET:-auto}${CGO_ENABLED:+, CGO_ENABLED=$CGO_ENABLED, CC=$CC})"
fi

# ── Effective target OS/arch for post-build packaging ───────────────
# Derive a normalized OS/arch even when --platform is not set, so packaging
# branches (dylib bundling, DLL copy) don't have to inspect filesystem state
# to figure out what was built. Stale Yansu.app dirs from previous local
# builds used to fool an [ -d Yansu.app ] check.
if [ -n "${TARGET_OS:-}" ]; then
    EFFECTIVE_TARGET_OS="$TARGET_OS"
else
    case "$(uname -s)" in
        Darwin) EFFECTIVE_TARGET_OS="darwin" ;;
        Linux)  EFFECTIVE_TARGET_OS="linux" ;;
        MINGW*|MSYS*|CYGWIN*) EFFECTIVE_TARGET_OS="windows" ;;
        *)      EFFECTIVE_TARGET_OS="$(uname -s | tr '[:upper:]' '[:lower:]')" ;;
    esac
fi
if [ -n "${TARGET_ARCH:-}" ]; then
    EFFECTIVE_TARGET_ARCH="$TARGET_ARCH"
else
    case "$(uname -m)" in
        arm64|aarch64) EFFECTIVE_TARGET_ARCH="arm64" ;;
        x86_64|amd64)  EFFECTIVE_TARGET_ARCH="amd64" ;;
        i386|i686|386) EFFECTIVE_TARGET_ARCH="386" ;;
        *)             EFFECTIVE_TARGET_ARCH="$(uname -m)" ;;
    esac
fi

if [ "$EFFECTIVE_TARGET_OS" = darwin ]; then
    # Wails otherwise inserts 10.13 into both CGO flags, below ONNX's 14.0.
    export MACOSX_DEPLOYMENT_TARGET=14.0
    export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
    export CGO_CXXFLAGS="${CGO_CXXFLAGS:-} -mmacosx-version-min=14.0"
    export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"
    if [ "$EFFECTIVE_TARGET_ARCH" = amd64 ]; then
        # The upstream Intel module's ORT 1.24.4 requires macOS 15.5.
        # Build a compatible runtime and place it before the module's -L path.
        bash "${SCRIPT_DIR}/build-onnxruntime-macos-x64.sh"
        onnx_override="${WAILS_DIR}/onnxruntime-bundle/lib"
        case "$onnx_override" in *"'"*) echo "[build-wails] Unsupported single quote in ONNX path" >&2; exit 1 ;; esac
        export CGO_LDFLAGS="'-L${onnx_override}' ${CGO_LDFLAGS}"
    fi
    sherpa_cache_valid() {
        local dir="$1" library
        for library in libsherpa-onnx-c-api.dylib libsherpa-onnx-cxx-api.dylib libonnxruntime.dylib libonnxruntime.1.24.4.dylib; do
            bundle_macho_matches "${dir}/${library}" "$EFFECTIVE_TARGET_ARCH" || return 1
        done
    }
    sherpa_check_dir="$(sherpa_module_lib_dir darwin "$EFFECTIVE_TARGET_ARCH")" || sherpa_check_dir=""
    if [ -z "$sherpa_check_dir" ] || ! sherpa_cache_valid "$sherpa_check_dir"; then
        # Never remove or rewrite a shared Go module cache. Re-fetch into an
        # owned cache, and let Go verify the module against go.sum.
        echo "[build-wails] Invalid sherpa-onnx cache; re-downloading into an isolated module cache"
        mkdir -p "${WAILS_DIR}/build"
        GOMODCACHE="$(mktemp -d "${WAILS_DIR}/build/sherpa-modcache.XXXXXXXX")"
        export GOMODCACHE
        (cd "$WAILS_DIR" && go mod download github.com/k2-fsa/sherpa-onnx-go-macos)
        sherpa_check_dir="$(sherpa_module_lib_dir darwin "$EFFECTIVE_TARGET_ARCH")"
        if ! sherpa_cache_valid "$sherpa_check_dir"; then
            echo "[build-wails] ERROR: re-downloaded sherpa-onnx libraries do not match ${EFFECTIVE_TARGET_ARCH}" >&2
            exit 1
        fi
    fi
    echo "[build-wails] Verified sherpa-onnx cache for ${EFFECTIVE_TARGET_ARCH}"
fi

# Build provenance for perf-snapshot diagnostics. Capture the source state
# before this script performs intentional tracked-file mutations such as
# injecting --version into wails.json; otherwise every release build reports
# dirty even when the checked-out source was clean.
REPO_ROOT="$(cd "$WAILS_DIR/.." && pwd)"
GIT_COMMIT_SHORT="$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
if git -C "$REPO_ROOT" diff --quiet HEAD -- 2>/dev/null; then
    GIT_DIRTY_FLAG=false
else
    GIT_DIRTY_FLAG=true
fi

# ── Version injection ───────────────────────────────────────────────
WAILS_JSON="${WAILS_DIR}/wails.json"
if [ -n "$VERSION" ]; then
    # Strip leading "v" if present (wails.json uses bare semver).
    VERSION="${VERSION#v}"
    echo "[build-wails] Injecting version ${VERSION} into wails.json"
    # Use a temp file for portable sed (macOS sed -i requires a backup suffix).
    sed "s/\"productVersion\": *\"[^\"]*\"/\"productVersion\": \"${VERSION}\"/" "$WAILS_JSON" > "${WAILS_JSON}.tmp"
    mv "${WAILS_JSON}.tmp" "$WAILS_JSON"
else
    # No --version flag: read version from wails.json so it gets injected via -ldflags.
    if [ -f "$WAILS_JSON" ]; then
        VERSION=$(sed -n 's/.*"productVersion"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$WAILS_JSON")
        if [ -n "$VERSION" ]; then
            echo "[build-wails] Using version from wails.json: $VERSION"
        fi
    fi
fi

# Bundle version keys must use Apple's numeric format. The complete semver is
# stored separately in signed metadata and in the Go binary.
BUNDLE_BUILD_NUMBER="$(date -u +%s)"

# 2. Build bundled yansu CLI.
# Rebuild whenever any yansu-agent Go source is newer than the bundled
# binary — without this, subcommands added after the bundle was last
# built (e.g. `yansu cron add`) silently go missing and the AI tells
# the user "this version of the CLI doesn't have cron". Mirrors the
# check in start-wails-gui.sh.
YANSU_AGENT_SRC="${WAILS_DIR}/../yansu-agent"
EXISTING_CLI_ENTRY=""
if [ -f "${CLI_BUNDLE}/bin/yansu.exe" ]; then
    EXISTING_CLI_ENTRY="${CLI_BUNDLE}/bin/yansu.exe"
elif [ -f "${CLI_BUNDLE}/bin/yansu" ]; then
    EXISTING_CLI_ENTRY="${CLI_BUNDLE}/bin/yansu"
fi
if [ "$SKIP_CLI" = true ]; then
    echo "[build-wails] Skipping bundled yansu CLI build (--skip-cli)"
elif [ -z "$EXISTING_CLI_ENTRY" ]; then
    echo "[build-wails] Bundled yansu CLI not found, building..."
    bash "${SCRIPT_DIR}/build-yansu-cli.sh"
elif [ "$EFFECTIVE_TARGET_OS" = darwin ] && ! bundle_macho_matches "$EXISTING_CLI_ENTRY" "$EFFECTIVE_TARGET_ARCH"; then
    echo "[build-wails] Yansu CLI target architecture changed; rebuilding..."
    bash "${SCRIPT_DIR}/build-yansu-cli.sh"
elif [ -d "$YANSU_AGENT_SRC" ] && [ -n "$(find "$YANSU_AGENT_SRC" -name '*.go' -newer "$EXISTING_CLI_ENTRY" -print -quit 2>/dev/null)" ]; then
    echo "[build-wails] Yansu CLI source changed since last build, rebuilding..."
    bash "${SCRIPT_DIR}/build-yansu-cli.sh"
else
    echo "[build-wails] Bundled yansu CLI is up to date"
fi

# Whisper.cpp bundle was replaced by sherpa-onnx (in-process via cgo); see
# bundle_sherpa_dylibs_macos / bundle_sherpa_dlls_windows below for the new
# native-lib bundling step. SKIP_WHISPER is preserved as a silent no-op for
# any external invocations that still pass --skip-whisper.

# 4. Download bun runtime (if not skipped and not already present).
if [ "$SKIP_BUN" = true ]; then
    echo "[build-wails] Skipping bun download (--skip-bun)"
elif [ "$EFFECTIVE_TARGET_OS" = darwin ]; then
    bash "${SCRIPT_DIR}/download-bun.sh" darwin "$EFFECTIVE_TARGET_ARCH"
elif [ -f "${BUN_BUNDLE}/bin/bun" ]; then
    if bun_bundle_arch_ok "${BUN_BUNDLE}/bin/bun"; then
        echo "[build-wails] Bun bundle already exists, skipping download"
    else
        echo "[build-wails] Bun bundle removed due to arch mismatch, downloading correct version..."
        bash "${SCRIPT_DIR}/download-bun.sh"
    fi
elif [ -f "${BUN_BUNDLE}/bin/bun.exe" ]; then
    echo "[build-wails] Bun bundle already exists, skipping download"
else
    echo "[build-wails] Bun bundle not found, downloading..."
    bash "${SCRIPT_DIR}/download-bun.sh"
fi

# 5. Download ffmpeg binary (if not skipped and not already present).
if [ "$SKIP_FFMPEG" = true ]; then
    echo "[build-wails] Skipping ffmpeg download (--skip-ffmpeg)"
elif [ "$EFFECTIVE_TARGET_OS" = darwin ]; then
    bash "${SCRIPT_DIR}/download-ffmpeg.sh" darwin "$EFFECTIVE_TARGET_ARCH"
elif [ -f "${FFMPEG_BUNDLE}/bin/ffmpeg" ] || [ -f "${FFMPEG_BUNDLE}/bin/ffmpeg.exe" ]; then
    echo "[build-wails] ffmpeg bundle already exists, skipping download"
else
    echo "[build-wails] ffmpeg bundle not found, downloading..."
    bash "${SCRIPT_DIR}/download-ffmpeg.sh"
fi

# OpenCLI is a pinned, complete package plus a native launcher. Always run
# staging so launcher source and target changes cannot reuse a stale binary.
GOOS="$EFFECTIVE_TARGET_OS" GOARCH="$EFFECTIVE_TARGET_ARCH" \
    bash "${SCRIPT_DIR}/download-opencli.sh"

# 5a. Download the pinned magpie gateway (macOS arm64 and amd64) for managed
#     model accounts. The embed step copies only the build's arch.
if [ "$SKIP_MAGPIE" = true ]; then
    echo "[build-wails] Skipping magpie download (--skip-magpie)"
else
    echo "[build-wails] Checking pinned magpie bundles..."
    bash "${SCRIPT_DIR}/download-magpie.sh"
fi

# 5c. Stage the pinned dumbpipe binaries for remote mobile access.
if [ "$SKIP_DUMBPIPE" = true ]; then
    echo "[build-wails] Skipping dumbpipe download (--skip-dumbpipe)"
else
    echo "[build-wails] Checking pinned dumbpipe bundles..."
    bash "${SCRIPT_DIR}/download-dumbpipe.sh"
fi

# The relay sidecar is a pure-Go Darwin executable and is built for the app's
# effective architecture. The generic nested-code signing walk below visits
# every Mach-O file under Contents, including both *-bundle resource folders.
if [ "$EFFECTIVE_TARGET_OS" = "darwin" ]; then
    mkdir -p "$RELAY_BUNDLE"
    echo "[build-wails] Building Darwin relay sidecar (${EFFECTIVE_TARGET_ARCH})..."
    (
        cd "${WAILS_DIR}/../relay"
        CGO_ENABLED=0 GOOS=darwin GOARCH="$EFFECTIVE_TARGET_ARCH" \
            go build -trimpath -ldflags="-s -w" \
                -o "${RELAY_BUNDLE}/isoform-relay-darwin-${EFFECTIVE_TARGET_ARCH}" ./cmd/isoform-relay
    )
fi

# 5b. Download PaddleOCR-json for the Windows OCR backend.
#     No-ops on non-Windows targets (download-paddleocr.sh exits 0 with a log).
if [ "$SKIP_PADDLEOCR" = true ]; then
    echo "[build-wails] Skipping PaddleOCR download (--skip-paddleocr)"
elif [ "$EFFECTIVE_TARGET_OS" != "windows" ]; then
    echo "[build-wails] PaddleOCR bundle skipped: not a Windows target"
elif [ -f "${PADDLEOCR_BUNDLE}/PaddleOCR-json.exe" ]; then
    echo "[build-wails] PaddleOCR bundle already exists, skipping download"
else
    echo "[build-wails] PaddleOCR bundle not found, downloading..."
    PADDLEOCR_TARGET="${EFFECTIVE_TARGET_OS}/${EFFECTIVE_TARGET_ARCH}" \
        bash "${SCRIPT_DIR}/download-paddleocr.sh"
fi

# 6a. Build cua-driver from vendored Swift sources (macOS arm64 only).
#     Source lives in wails-gui/cua-driver-src/. We unconditionally rebuild
#     so source edits flow through without manual stale-bundle cleanup;
#     swiftc on incremental builds is fast (~5–10s for the full helper).
if [ "$SKIP_CUA_DRIVER" = true ]; then
    echo "[build-wails] Skipping cua-driver build (--skip-cua-driver)"
elif [ "$EFFECTIVE_TARGET_OS" = "darwin" ] && [ "$EFFECTIVE_TARGET_ARCH" = "arm64" ]; then
    echo "[build-wails] Building cua-driver from vendored source..."
    bash "${SCRIPT_DIR}/build-cua-driver-bundle.sh"
else
    echo "[build-wails] cua-driver is darwin-arm64 only; skipping for ${EFFECTIVE_TARGET_OS}/${EFFECTIVE_TARGET_ARCH}"
fi

# 7b. Download portable git (if not skipped and not already present).
#     On Windows: MinGit from git-for-windows. On macOS/Linux: system git is
#     expected; the build bundles it from the host if available.
GIT_BUNDLE_VALID=false
if [ "$SKIP_GIT" = true ]; then
    echo "[build-wails] Skipping git download (--skip-git)"
else
    GIT_TARGET_PLATFORM="$(git_target_platform)"
    if git_bundle_matches_target "$GIT_TARGET_PLATFORM" && { [ "$EFFECTIVE_TARGET_OS" != darwin ] || { bundle_macho_matches "${GIT_BUNDLE}/bin/git" "$EFFECTIVE_TARGET_ARCH" && bundle_macho_tree_matches "$GIT_BUNDLE" "$EFFECTIVE_TARGET_ARCH"; }; }; then
        echo "[build-wails] git bundle already exists for ${GIT_TARGET_PLATFORM}, skipping download"
    else
        echo "[build-wails] git bundle missing or built for a different target, rebuilding for ${GIT_TARGET_PLATFORM}..."
        bash "${SCRIPT_DIR}/download-git.sh"
    fi
    if git_bundle_matches_target "$GIT_TARGET_PLATFORM" && { [ "$EFFECTIVE_TARGET_OS" != darwin ] || { bundle_macho_matches "${GIT_BUNDLE}/bin/git" "$EFFECTIVE_TARGET_ARCH" && bundle_macho_tree_matches "$GIT_BUNDLE" "$EFFECTIVE_TARGET_ARCH"; }; }; then
        GIT_BUNDLE_VALID=true
    fi
fi

# 8. Ensure app icon is set (Wails uses build/appicon.png for .icns/.ico).
#    The version-controlled PNG is the supplied Violet macOS 1024 render.
APPICON_SRC="${WAILS_DIR}/assets/appicon.png"
APPICON_DST="${WAILS_DIR}/build/appicon.png"
mkdir -p "${WAILS_DIR}/build"
if [ -f "$APPICON_SRC" ]; then
    cp "$APPICON_SRC" "$APPICON_DST"
    echo "[build-wails] Using Isoform app icon: $APPICON_DST"
else
    echo "[build-wails] Warning: app icon source not found ($APPICON_SRC), Wails may use default icon"
fi

if [ "$EFFECTIVE_TARGET_OS" = "darwin" ]; then
    bash "${SCRIPT_DIR}/build-macos-icon.sh"
fi

# 8. Build the Wails app.
if ! command -v wails &>/dev/null; then
    echo "[build-wails] Error: wails not found. Install via: go install github.com/wailsapp/wails/v2/cmd/wails@latest" >&2
    exit 1
fi

WAILS_BUILD_ARGS=()
# Runner-only instrumentation. Production release workflows never set this.
if [ "${YANSU_UPDATE_QA_BUILD:-0}" = "1" ]; then
    WAILS_BUILD_ARGS+=("-tags" "updateqa")
fi
if [ -n "$PLATFORM" ]; then
    WAILS_BUILD_ARGS+=("-platform" "$PLATFORM")
fi
# Every produced binary carries its commit hash, dirty flag, and build
# timestamp so a shared snapshot can be matched to source without asking the
# user. GIT_DIRTY_FLAG was captured before script-owned mutations above.
BUILD_TIME_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Assemble -ldflags: version + provenance + any extra flags.
LDFLAGS_PARTS=""
if [ -n "$VERSION" ]; then
    LDFLAGS_PARTS="-X main.Version=$VERSION"
fi
LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.GitCommit=$GIT_COMMIT_SHORT -X main.GitDirty=$GIT_DIRTY_FLAG -X main.BuildTime=$BUILD_TIME_ISO"
# PostHog's project key is ingest-only and designed to ship to clients.
# Embed only the explicitly supplied desktop project config, without logging
# its values. Empty build defaults disable telemetry; there is no runtime env
# fallback. Reject characters that would split or re-quote Go's -ldflags string.
validate_posthog_build_value() {
    case "$2" in
        *[[:space:]]*|*"'"*|*'"'*|*\\*)
            echo "[build-wails] Error: $1 must not contain whitespace, quotes, or backslashes" >&2
            exit 2
            ;;
    esac
}
# ISOFORM_DESKTOP_POSTHOG_* win; YANSU_DESKTOP_POSTHOG_* are a deprecated
# fallback. The resolved value is exported under the Isoform name. Comparisons
# use builtins, so the key never appears in a process's arguments.
resolve_posthog_build_value() {
    local name="ISOFORM_DESKTOP_POSTHOG_$1" legacy="YANSU_DESKTOP_POSTHOG_$1"
    local value="${!name:-}" legacy_value="${!legacy:-}"
    if [ -n "$value" ] && [ -n "$legacy_value" ] && [ "$value" != "$legacy_value" ]; then
        echo "[build-wails] Error: $name and $legacy are both set with different values" >&2
        exit 2
    fi
    if [ -z "$value" ] && [ -n "$legacy_value" ]; then
        echo "[build-wails] Deprecated $legacy; use $name" >&2
        value="$legacy_value"
    fi
    export "$name=$value"
}
resolve_posthog_build_value KEY
resolve_posthog_build_value HOST
if [ -n "$ISOFORM_DESKTOP_POSTHOG_KEY" ]; then
    validate_posthog_build_value ISOFORM_DESKTOP_POSTHOG_KEY "$ISOFORM_DESKTOP_POSTHOG_KEY"
    LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.DefaultDesktopPostHogKey=${ISOFORM_DESKTOP_POSTHOG_KEY}"
fi
if [ -n "$ISOFORM_DESKTOP_POSTHOG_HOST" ]; then
    validate_posthog_build_value ISOFORM_DESKTOP_POSTHOG_HOST "$ISOFORM_DESKTOP_POSTHOG_HOST"
    LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.DefaultDesktopPostHogHost=${ISOFORM_DESKTOP_POSTHOG_HOST}"
fi
# An explicitly configured relay URL disables the local remote-access relay.
desktop_relay_url="${ISOFORM_DESKTOP_RELAY_URL:-}"
if [ -n "$desktop_relay_url" ] && [ -n "${YANSU_DESKTOP_RELAY_URL:-}" ] && [ "$desktop_relay_url" != "$YANSU_DESKTOP_RELAY_URL" ]; then
    echo "[build-wails] Error: ISOFORM_DESKTOP_RELAY_URL and YANSU_DESKTOP_RELAY_URL are both set with different values" >&2
    exit 2
fi
if [ -z "$desktop_relay_url" ] && [ -n "${YANSU_DESKTOP_RELAY_URL:-}" ]; then
    echo "[build-wails] Deprecated YANSU_DESKTOP_RELAY_URL; use ISOFORM_DESKTOP_RELAY_URL" >&2
    desktop_relay_url="$YANSU_DESKTOP_RELAY_URL"
fi
if [ -n "$desktop_relay_url" ]; then
    validate_posthog_build_value ISOFORM_DESKTOP_RELAY_URL "$desktop_relay_url"
    case "$desktop_relay_url" in
        https://*) ;;
        *)
            echo "[build-wails] Error: ISOFORM_DESKTOP_RELAY_URL must be an https:// origin" >&2
            exit 2
            ;;
    esac
    LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.DefaultMobileRelayURL=${desktop_relay_url}"
fi
case "${ISOFORM_DESKTOP_REMOTE_ACCESS:-}" in
    0|1)
        LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.DefaultMobileRemoteAccess=${ISOFORM_DESKTOP_REMOTE_ACCESS}"
        ;;
esac
if [ -n "${ISOFORM_DESKTOP_PUSH_GATEWAY_URL:-}" ]; then
    validate_posthog_build_value ISOFORM_DESKTOP_PUSH_GATEWAY_URL "$ISOFORM_DESKTOP_PUSH_GATEWAY_URL"
    case "$ISOFORM_DESKTOP_PUSH_GATEWAY_URL" in
        https://*) ;;
        *)
            echo "[build-wails] Error: ISOFORM_DESKTOP_PUSH_GATEWAY_URL must be an https:// origin" >&2
            exit 2
            ;;
    esac
    push_gateway_environment="${ISOFORM_DESKTOP_PUSH_GATEWAY_ENVIRONMENT:-production}"
    case "$push_gateway_environment" in
        development|production) ;;
        *)
            echo "[build-wails] Error: ISOFORM_DESKTOP_PUSH_GATEWAY_ENVIRONMENT must be development or production" >&2
            exit 2
            ;;
    esac
    validate_posthog_build_value ISOFORM_DESKTOP_PUSH_GATEWAY_ENVIRONMENT "$push_gateway_environment"
    push_gateway_public_key="${ISOFORM_DESKTOP_PUSH_GATEWAY_PUBLIC_KEY:-}"
    if [ -z "$push_gateway_public_key" ]; then
        echo "ISOFORM_DESKTOP_PUSH_GATEWAY_PUBLIC_KEY is required with ISOFORM_DESKTOP_PUSH_GATEWAY_URL" >&2
        exit 1
    fi
    validate_posthog_build_value ISOFORM_DESKTOP_PUSH_GATEWAY_PUBLIC_KEY "$push_gateway_public_key"
    LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.DefaultMobilePushGatewayURL=${ISOFORM_DESKTOP_PUSH_GATEWAY_URL} -X main.DefaultMobilePushGatewayEnvironment=${push_gateway_environment} -X main.DefaultMobilePushGatewayPublicKey=${push_gateway_public_key}"
fi
# Both builds are compiled as Isoform; only the release carries the release
# channel, which turns on update checks (app_flavor.go isReleaseChannel).
LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.buildFlavor=isoform"
if [ "$FLAVOR" = "isoform" ]; then
    echo "[build-wails] FLAVOR isoform: development build, runs as Isoform with data in ~/.isoform-agent, no update checks"
else
    LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }-X main.buildChannel=release"
    echo "[build-wails] Release build: runs as Isoform with data in ~/.isoform-agent"
fi
if [ -n "$EXTRA_LDFLAGS" ]; then
    LDFLAGS_PARTS="${LDFLAGS_PARTS:+$LDFLAGS_PARTS }$EXTRA_LDFLAGS"
fi
if [ -n "$LDFLAGS_PARTS" ]; then
    WAILS_BUILD_ARGS+=("-ldflags" "$LDFLAGS_PARTS")
fi

# 8b. Build the Quick Chat Spotlight helper (~/quick-chat-spotlight/).
#     The helper is a separate binary Isoform spawns at startup to
#     host the pill + conversation floating panels (see DESIGN.md).
#     --skip-spotlight remains an explicit opt-out (CI fast paths).
#     Otherwise failures require YANSU_ALLOW_NO_SPOTLIGHT=1 to continue
#     with the ShowQuickChatWindow fallback and no bundled helper.
spotlight_build_failed() {
    if [ "${YANSU_ALLOW_NO_SPOTLIGHT:-0}" = "1" ]; then
        echo "[build-wails] WARNING: $1; helper will not be bundled (YANSU_ALLOW_NO_SPOTLIGHT=1)." >&2
        return 0
    fi
    echo "[build-wails] ERROR: $1; refusing to build without Quick Chat Spotlight. Set YANSU_ALLOW_NO_SPOTLIGHT=1 to allow the in-app fallback." >&2
    return 1
}

build_quick_chat_spotlight() {
    SPOTLIGHT_BUILT=false
    if [ "$SKIP_SPOTLIGHT" = true ]; then
        echo "[build-wails] Skipping quick-chat-spotlight (--skip-spotlight)"
        return 0
    fi
    if [ ! -d "$SPOTLIGHT_DIR" ]; then
        spotlight_build_failed "quick-chat-spotlight source directory missing"
        return $?
    fi
    if ! command -v pnpm >/dev/null 2>&1; then
        spotlight_build_failed "pnpm not found on PATH"
        return $?
    fi

    local spotlight_platform spotlight_os spotlight_arch
    spotlight_platform="${PLATFORM:-${EFFECTIVE_TARGET_OS}/${EFFECTIVE_TARGET_ARCH}}"
    spotlight_os="${spotlight_platform%%/*}"
    spotlight_arch="${spotlight_platform##*/}"

    echo "[build-wails] Building quick-chat-spotlight for ${spotlight_platform}..."

    # Install main frontend deps BEFORE the Spotlight frontend: its shared
    # styles/components resolve packages (including tailwindcss) from there.
    # Waiting for the later `wails build` install breaks a fresh checkout.
    if ! (cd "${WAILS_DIR}/frontend" && pnpm install); then
        spotlight_build_failed "main frontend dependency install failed before the Spotlight build"
        return $?
    fi

    if [ "$spotlight_os" = "darwin" ]; then
        # macOS: native AppKit NSPanel app (no Wails — see run_darwin.go). The
        # non-activating panel doesn't steal focus from the user's frontmost
        # app. Built as vite (frontend) + go (binary) + a hand-written .app
        # bundle, matching the layout install_quick_chat_spotlight expects.
        local app_dir macos_dir
        app_dir="${SPOTLIGHT_DIR}/build/bin/quick-chat-spotlight.app"
        macos_dir="${app_dir}/Contents/MacOS"
        if ! (cd "${SPOTLIGHT_DIR}/frontend" && pnpm install && pnpm run build); then
            spotlight_build_failed "spotlight frontend build failed"
            return $?
        fi
        mkdir -p "$macos_dir"
        if ! (cd "$SPOTLIGHT_DIR" &&
            GOOS=darwin GOARCH="$spotlight_arch" CGO_ENABLED=1 \
                go build -o "${macos_dir}/quick-chat-spotlight" .); then
            spotlight_build_failed "spotlight Go helper build failed"
            return $?
        fi
        if [ ! -f "${macos_dir}/quick-chat-spotlight" ]; then
            spotlight_build_failed "expected spotlight binary at ${macos_dir}/quick-chat-spotlight but it is missing"
            return $?
        fi
        write_spotlight_info_plist "${app_dir}/Contents/Info.plist"
        mark_spotlight_helper_lsui_element "${app_dir}/Contents/Info.plist"
        SPOTLIGHT_BUILT=true
        return 0
    fi

    # Windows/Linux: still a Wails app.
    if ! command -v wails >/dev/null 2>&1; then
        spotlight_build_failed "wails CLI not found on PATH"
        return $?
    fi
    if ! (
        # frontend/wailsjs/ is gitignored (auto-generated from app.go bindings);
        # let wails regenerate it so the Vite build can resolve the runtime import.
        cd "$SPOTLIGHT_DIR" && wails build -platform "$spotlight_platform"
    ); then
        spotlight_build_failed "spotlight Wails helper build failed"
        return $?
    fi
    local helper_name="quick-chat-spotlight"
    if [ "$spotlight_os" = "windows" ]; then
        helper_name="${helper_name}.exe"
    fi
    if [ ! -f "${SPOTLIGHT_DIR}/build/bin/${helper_name}" ]; then
        spotlight_build_failed "expected spotlight binary at ${SPOTLIGHT_DIR}/build/bin/${helper_name} but it is missing"
        return $?
    fi
    SPOTLIGHT_BUILT=true
}

build_darwin_ocr_helper() {
    if [ "$EFFECTIVE_TARGET_OS" != "darwin" ]; then
        return 0
    fi

    local helper_out
    helper_out="${WAILS_DIR}/build/bin/${OCR_HELPER_NAME}"
    mkdir -p "$(dirname "$helper_out")"

    echo "[build-wails] Building OCR helper for darwin/${EFFECTIVE_TARGET_ARCH}..."
    (
        cd "$WAILS_DIR"
        GOOS=darwin GOARCH="$EFFECTIVE_TARGET_ARCH" CGO_ENABLED=1 \
            go build -o "$helper_out" ./cmd/ocr-helper
    )
}

build_darwin_pii_helper() {
    if [ "$EFFECTIVE_TARGET_OS" != "darwin" ]; then
        return 0
    fi

    local helper_out
    helper_out="${WAILS_DIR}/build/bin/${PII_HELPER_NAME}"
    mkdir -p "$(dirname "$helper_out")"

    echo "[build-wails] Building PII helper for darwin/${EFFECTIVE_TARGET_ARCH}..."
    (
        cd "$WAILS_DIR"
        GOOS=darwin GOARCH="$EFFECTIVE_TARGET_ARCH" CGO_ENABLED=1 \
            go build -o "$helper_out" ./cmd/pii-helper
    )
}

remove_bundled_quick_chat_spotlight() {
    if [ "$EFFECTIVE_TARGET_OS" = "darwin" ]; then
        rm -rf "${APP_PATH}/Contents/Resources/quick-chat-spotlight.app"
        rm -f "${APP_PATH}/Contents/MacOS/quick-chat-spotlight" \
            "${APP_PATH}/Contents/Resources/quick-chat-spotlight"
    else
        rm -f "${WAILS_DIR}/build/bin/quick-chat-spotlight" \
            "${WAILS_DIR}/build/bin/quick-chat-spotlight.exe"
    fi
}

# install_quick_chat_spotlight copies the helper produced by
# build_quick_chat_spotlight into the location Yansu's
# spotlightSupervisor looks at:
#   - macOS: <Yansu.app>/Contents/Resources/quick-chat-spotlight.app
#   - Windows/Linux: <build/bin>/quick-chat-spotlight[.exe]
# Runs AFTER `wails build` so APP_PATH exists, but BEFORE the macOS
# code-signing loop so the helper gets signed alongside the main app.
install_quick_chat_spotlight() {
    # Never copy a stale helper from a prior build after an allowed failure.
    if [ "$SPOTLIGHT_BUILT" != true ]; then
        # Also remove a helper left in the main app's existing output tree.
        remove_bundled_quick_chat_spotlight
        return $?
    fi

    local helper_name helper_src
    helper_name="quick-chat-spotlight"
    if [ "$EFFECTIVE_TARGET_OS" = "windows" ]; then
        helper_name="${helper_name}.exe"
    fi
    if [ "$EFFECTIVE_TARGET_OS" = "darwin" ]; then
        # Wails packages macOS builds as an .app bundle, so the executable is
        # nested under Contents/MacOS rather than emitted directly in build/bin.
        helper_src="${SPOTLIGHT_DIR}/build/bin/quick-chat-spotlight.app/Contents/MacOS/${helper_name}"
    else
        helper_src="${SPOTLIGHT_DIR}/build/bin/${helper_name}"
    fi

    if [ ! -f "$helper_src" ]; then
        spotlight_build_failed "expected spotlight binary at ${helper_src} but it is missing" || return $?
        SPOTLIGHT_BUILT=false
        remove_bundled_quick_chat_spotlight
        return $?
    fi

    local helper_dest_dir helper_dest
    if [ "$EFFECTIVE_TARGET_OS" = "darwin" ]; then
        local helper_app_src helper_app_dest helper_plist
        helper_app_src="${SPOTLIGHT_DIR}/build/bin/quick-chat-spotlight.app"
        helper_app_dest="${APP_PATH}/Contents/Resources/quick-chat-spotlight.app"

        # Remove legacy flat layouts from older local builds. If left in
        # place, the supervisor can spawn the helper from Contents/MacOS,
        # which makes LaunchServices treat it as a second Yansu.app.
        rm -f "${APP_PATH}/Contents/MacOS/${helper_name}"
        rm -f "${APP_PATH}/Contents/Resources/${helper_name}"
        rm -rf "$helper_app_dest"

        if [ -d "$helper_app_src" ]; then
            ditto "$helper_app_src" "$helper_app_dest"
            helper_plist="${helper_app_dest}/Contents/Info.plist"
            mark_spotlight_helper_lsui_element "$helper_plist"
            chmod +x "${helper_app_dest}/Contents/MacOS/${helper_name}" 2>/dev/null || true
            echo "[build-wails] Installed quick-chat-spotlight app to ${helper_app_dest}"
            return 0
        fi

        echo "[build-wails] WARNING: expected spotlight app bundle at ${helper_app_src} but it is missing; falling back to legacy flat helper." >&2
        helper_dest_dir="${APP_PATH}/Contents/Resources"
    else
        helper_dest_dir="${WAILS_DIR}/build/bin"
    fi
    mkdir -p "$helper_dest_dir"
    helper_dest="${helper_dest_dir}/${helper_name}"
    cp -f "$helper_src" "$helper_dest"
    chmod +x "$helper_dest" 2>/dev/null || true
    echo "[build-wails] Installed quick-chat-spotlight to ${helper_dest}"
}

install_darwin_ocr_helper() {
    if [ "$EFFECTIVE_TARGET_OS" != "darwin" ]; then
        return 0
    fi

    local helper_src helper_dest
    helper_src="${WAILS_DIR}/build/bin/${OCR_HELPER_NAME}"
    helper_dest="${APP_PATH}/Contents/Resources/${OCR_HELPER_NAME}"

    if [ ! -f "$helper_src" ]; then
        echo "[build-wails] WARNING: expected OCR helper at ${helper_src} but it is missing; skipping copy." >&2
        return 0
    fi

    cp -f "$helper_src" "$helper_dest"
    chmod +x "$helper_dest" 2>/dev/null || true
    echo "[build-wails] Installed OCR helper to ${helper_dest}"
}

install_darwin_pii_helper() {
    if [ "$EFFECTIVE_TARGET_OS" != "darwin" ]; then
        return 0
    fi

    local helper_src helper_dest
    helper_src="${WAILS_DIR}/build/bin/${PII_HELPER_NAME}"
    helper_dest="${APP_PATH}/Contents/Resources/${PII_HELPER_NAME}"

    if [ ! -f "$helper_src" ]; then
        echo "[build-wails] WARNING: expected PII helper at ${helper_src} but it is missing; skipping copy." >&2
        return 0
    fi

    cp -f "$helper_src" "$helper_dest"
    chmod +x "$helper_dest" 2>/dev/null || true
    echo "[build-wails] Installed PII helper to ${helper_dest}"
}

# Build the helper before `wails build` to keep failure detection early
# (faster feedback than waiting through Wails' bindings step). The copy
# happens later via install_quick_chat_spotlight, after wails build has
# created the macOS .app bundle.
build_quick_chat_spotlight
build_darwin_ocr_helper
build_darwin_pii_helper

echo "[build-wails] Building Wails app${PLATFORM:+ for $PLATFORM}..."
cd "$WAILS_DIR"

# When building for Windows, Wails' binding generator compiles a cgo binary
# from the project's main package and runs it for type introspection. Since
# we have cgo deps on sherpa-onnx-go-windows, that intermediate binary needs
# the three sherpa DLLs (sherpa-onnx-c-api.dll / sherpa-onnx-cxx-api.dll /
# onnxruntime.dll) reachable via the Windows DLL search path at runtime.
#
# Two failure modes to defend against:
#   1. STATUS_DLL_NOT_FOUND (0xc0000135) — DLLs not on the search path at all.
#      Fixed by prepending the sherpa lib dir to PATH.
#   2. STATUS_INVALID_IMAGE_FORMAT (0xc000007b) — DLL found but wrong
#      version. C:\Windows\System32 ships a 2832-byte onnxruntime.dll stub
#      (Microsoft WindowsML API-set proxy) that shares the name but is
#      ABI-incompatible with the real 1.24.x build sherpa links against.
#      Windows DLL search puts System32 ABOVE PATH (SafeDllSearchMode
#      default), so prepending to PATH does NOT win this race — the loader
#      grabs the broken stub and crashes the binding-gen exe.
#
# Fix for (2): stage the real DLLs into the binding-gen exe's APPLICATION
# DIRECTORY, which is search-order #1 (above System32). Wails v2 writes
# wailsbindings.exe directly to os.TempDir() — confirmed in
# github.com/wailsapp/wails/v2/pkg/commands/bindings/bindings.go — so the
# real %TEMP% is where the DLLs need to land. MSYS bash remaps $TEMP/$TMP
# to /tmp, so query Windows %TEMP% via cmd.exe instead of trusting $TEMP.
if [ "$EFFECTIVE_TARGET_OS" = "windows" ]; then
    # sherpa_module_lib_dir already returns an MSYS-style path on Windows
    # hosts (it does the cygpath conversion internally), so prepending with
    # `:` is safe for bash splitting and MSYS will translate it back to
    # Windows form when spawning the binding-gen exe.
    if sherpa_win_lib_dir="$(sherpa_module_lib_dir "windows" "$EFFECTIVE_TARGET_ARCH")"; then
        export PATH="${sherpa_win_lib_dir}:${PATH}"
        echo "[build-wails] Prepended sherpa-onnx lib dir to PATH for binding generation: ${sherpa_win_lib_dir}"

        win_temp="$(cmd.exe //c "echo %TEMP%" 2>/dev/null | tr -d '\r\n')"
        bind_stage_dir=""
        if [ -n "$win_temp" ] && command -v cygpath >/dev/null 2>&1; then
            bind_stage_dir="$(cygpath -u "$win_temp" 2>/dev/null)"
        fi
        if [ -n "$bind_stage_dir" ] && [ -d "$bind_stage_dir" ]; then
            cp -f "${sherpa_win_lib_dir}/sherpa-onnx-c-api.dll"   "$bind_stage_dir/" 2>/dev/null || true
            cp -f "${sherpa_win_lib_dir}/sherpa-onnx-cxx-api.dll" "$bind_stage_dir/" 2>/dev/null || true
            cp -f "${sherpa_win_lib_dir}/onnxruntime.dll"         "$bind_stage_dir/" 2>/dev/null || true
            echo "[build-wails] Staged sherpa DLLs into wails binding-gen app dir: ${bind_stage_dir} (workaround for System32 onnxruntime stub)"
        else
            echo "[build-wails] WARNING: could not resolve Windows %TEMP%; if System32 contains an onnxruntime.dll stub, binding generation will fail with 0xc000007b" >&2
        fi
    else
        echo "[build-wails] WARNING: failed to locate sherpa-onnx lib dir; binding generation will likely fail with STATUS_DLL_NOT_FOUND" >&2
    fi
fi

# wails.json's post-build hook (scripts/sign-dev-build.sh) signs the wails dev
# bundle when start-wails-gui.sh exported these; this script signs the bundle
# itself below, so a value leaked from the shell or .env.local must not reach
# the hook.
unset YANSU_DEV_SIGNING YANSU_KEEP_PERMISSIONS
# The wails.json post-build hook (scripts/sign-dev-build.sh) is for wails dev;
# this script signs the bundle itself after embedding.
if [ -n "$ISOFORM_DESKTOP_POSTHOG_KEY" ]; then
    # wails build prints its -ldflags, which then carry the PostHog key. Redact
    # the flag and the literal value (read from the environment, never from
    # arguments) so the key stays out of terminal and release logs.
    YANSU_BUILD_WAILS=1 wails build "${WAILS_BUILD_ARGS[@]+"${WAILS_BUILD_ARGS[@]}"}" 2>&1 | awk '
        BEGIN { key = ENVIRON["ISOFORM_DESKTOP_POSTHOG_KEY"] }
        {
            while ((i = index($0, key)) > 0) $0 = substr($0, 1, i - 1) "[redacted]" substr($0, i + length(key))
            gsub(/main\.DefaultDesktopPostHogKey=[^ "]*/, "main.DefaultDesktopPostHogKey=[redacted]")
            print
            fflush()
        }'
else
    YANSU_BUILD_WAILS=1 wails build "${WAILS_BUILD_ARGS[@]+"${WAILS_BUILD_ARGS[@]}"}"
fi

# 9. Copy runtime bundles next to the built binary.
#    - macOS: embed into Isoform.app/Contents/Resources/
#    - Windows/Linux: copy next to the executable in build/bin/
if [ "$EFFECTIVE_TARGET_OS" = "darwin" ]; then
    if [ ! -d "$APP_PATH" ]; then
        echo "[build-wails] Error: expected macOS app bundle at ${APP_PATH} but it is missing." >&2
        exit 1
    fi

    # Stamp the Isoform identity on both builds (wails.json already sets the
    # bundle id and name; this adds CFBundleDisplayName and keeps an older
    # wails.json from producing another identity). The inner executable
    # stays "$EXE_BASENAME".
    if command -v /usr/libexec/PlistBuddy &>/dev/null; then
        FLAVOR_PLIST="${APP_PATH}/Contents/Info.plist"
        /usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier com.wails.Isoform" "$FLAVOR_PLIST" 2>/dev/null \
            || /usr/libexec/PlistBuddy -c "Add :CFBundleIdentifier string com.wails.Isoform" "$FLAVOR_PLIST"
        /usr/libexec/PlistBuddy -c "Set :CFBundleName Isoform" "$FLAVOR_PLIST" 2>/dev/null \
            || /usr/libexec/PlistBuddy -c "Add :CFBundleName string Isoform" "$FLAVOR_PLIST"
        /usr/libexec/PlistBuddy -c "Set :CFBundleDisplayName Isoform" "$FLAVOR_PLIST" 2>/dev/null \
            || /usr/libexec/PlistBuddy -c "Add :CFBundleDisplayName string Isoform" "$FLAVOR_PLIST"
        FLAVOR_APP_PATH="${WAILS_DIR}/build/bin/Isoform.app"
        if [ "$FLAVOR_APP_PATH" != "$APP_PATH" ]; then
            rm -rf "$FLAVOR_APP_PATH"
            mv "$APP_PATH" "$FLAVOR_APP_PATH"
            APP_NAME="Isoform.app"
            APP_PATH="$FLAVOR_APP_PATH"
        fi
        echo "[build-wails] Stamped 'Isoform' identity (bundle id com.wails.Isoform)"
    fi

    # macOS .app bundle
    RESOURCES_DIR="${APP_PATH}/Contents/Resources"
    INFO_PLIST="${APP_PATH}/Contents/Info.plist"

    if [ -n "$VERSION" ]; then
        stamp_macos_bundle_versions "$INFO_PLIST"
    fi

    # Mark the private login-item protocol before signing; callers inspect this
    # metadata rather than executing an older installed binary as a handshake.
    /usr/libexec/PlistBuddy -c "Set :YansuLoginItemProtocolVersion 1" "$INFO_PLIST" 2>/dev/null \
        || /usr/libexec/PlistBuddy -c "Add :YansuLoginItemProtocolVersion integer 1" "$INFO_PLIST"
    /usr/bin/plutil -convert xml1 "$INFO_PLIST"
    if [ "$INSTALL_APP" = true ]; then
        mkdir -p "$RESOURCES_DIR"
        printf '%s:%s\n' "$INSTALL_MARKER_TOKEN" "$APP_NAME" > "${RESOURCES_DIR}/.isoform-build-install"
    else
        # An ordinary/release build must not inherit a previous install marker.
        rm -f "${RESOURCES_DIR}/.isoform-build-install"
    fi

    # Add microphone + system audio capture permissions for local transcription.
    if [ -f "$INFO_PLIST" ] && command -v /usr/libexec/PlistBuddy &>/dev/null; then
        /usr/libexec/PlistBuddy -c "Add :NSMicrophoneUsageDescription string 'Isoform uses the microphone for voice typing and, while listening is on, to hear what you say. Audio is transcribed on this computer.'" "$INFO_PLIST" 2>/dev/null || true
        /usr/libexec/PlistBuddy -c "Add :NSAudioCaptureUsageDescription string 'Isoform captures the sound your computer plays, such as the other side of a call, and transcribes it on this computer while listening is on.'" "$INFO_PLIST" 2>/dev/null || true
        echo "[build-wails] Added NSMicrophoneUsageDescription to Info.plist"
    fi

    # Localized permission prompts (en, zh-Hans): macOS shows the usage
    # descriptions in the system language from <lang>.lproj/InfoPlist.strings.
    for lproj in "${WAILS_DIR}/build/darwin/"*.lproj; do
        [ -d "$lproj" ] || continue
        rm -rf "${RESOURCES_DIR}/$(basename "$lproj")"
        cp -R "$lproj" "${RESOURCES_DIR}/"
    done
    echo "[build-wails] Installed localized permission prompts"

    if [ -d "$CLI_BUNDLE" ]; then
        echo "[build-wails] Embedding bundled yansu CLI into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/yansu-cli-bundle"
        cp -r "$CLI_BUNDLE" "${RESOURCES_DIR}/yansu-cli-bundle"
    else
        echo "[build-wails] Warning: bundled yansu CLI not found, skipping CLI embed"
    fi

    if [ "$GIT_BUNDLE_VALID" = true ]; then
        echo "[build-wails] Embedding git bundle into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/git-bundle"
        # Preserve git-core symlinks; macOS cp -r can expand them into full files.
        cp -RP "$GIT_BUNDLE" "${RESOURCES_DIR}/git-bundle"
        find "${RESOURCES_DIR}/git-bundle" -type l ! -exec test -e {} \; -delete 2>/dev/null || true
    else
        rm -rf "${RESOURCES_DIR}/git-bundle"
        echo "[build-wails] Warning: git bundle not found, skipping git embed"
    fi

    if [ -d "$BUN_BUNDLE" ]; then
        echo "[build-wails] Embedding bun bundle into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/bun-bundle"
        cp -r "$BUN_BUNDLE" "${RESOURCES_DIR}/bun-bundle"
    else
        echo "[build-wails] Warning: bun bundle not found, skipping bun embed"
    fi

    if [ -d "$FFMPEG_BUNDLE" ]; then
        echo "[build-wails] Embedding ffmpeg bundle into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/ffmpeg-bundle"
        cp -r "$FFMPEG_BUNDLE" "${RESOURCES_DIR}/ffmpeg-bundle"
    else
        echo "[build-wails] Warning: ffmpeg bundle not found, skipping ffmpeg embed"
    fi

    echo "[build-wails] Embedding pinned OpenCLI bundle into ${APP_NAME}..."
    rm -rf "${RESOURCES_DIR}/opencli-bundle"
    cp -R "$OPENCLI_BUNDLE" "${RESOURCES_DIR}/opencli-bundle"

    # magpie gateway: embed the binary for this build's arch as
    # Contents/Resources/magpie-bundle/magpie-cli (LocateBundledMagpie searches
    # there). The embedded-code walk in sign_macos_embedded_code signs it with
    # the other nested Mach-O binaries.
    magpie_src="${MAGPIE_BUNDLE}/magpie-cli-darwin-${EFFECTIVE_TARGET_ARCH}"
    if [ -f "$magpie_src" ]; then
        echo "[build-wails] Embedding magpie gateway (${EFFECTIVE_TARGET_ARCH}) into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/magpie-bundle"
        mkdir -p "${RESOURCES_DIR}/magpie-bundle"
        cp "$magpie_src" "${RESOURCES_DIR}/magpie-bundle/magpie-cli"
        chmod +x "${RESOURCES_DIR}/magpie-bundle/magpie-cli"
    else
        echo "[build-wails] Warning: magpie binary for ${EFFECTIVE_TARGET_ARCH} not found, skipping magpie embed (managed model accounts will be unavailable)"
    fi

    dumbpipe_src="${DUMBPIPE_BUNDLE}/dumbpipe-darwin-${EFFECTIVE_TARGET_ARCH}"
    if [ -f "$dumbpipe_src" ]; then
        echo "[build-wails] Embedding dumbpipe (${EFFECTIVE_TARGET_ARCH}) into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/dumbpipe-bundle"
        mkdir -p "${RESOURCES_DIR}/dumbpipe-bundle"
        cp "$dumbpipe_src" "${RESOURCES_DIR}/dumbpipe-bundle/dumbpipe"
        chmod +x "${RESOURCES_DIR}/dumbpipe-bundle/dumbpipe"
    else
        echo "[build-wails] Warning: dumbpipe for ${EFFECTIVE_TARGET_ARCH} not found, skipping dumbpipe embed"
    fi
    relay_src="${RELAY_BUNDLE}/isoform-relay-darwin-${EFFECTIVE_TARGET_ARCH}"
    if [ -f "$relay_src" ]; then
        echo "[build-wails] Embedding relay sidecar into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/relay-bundle"
        mkdir -p "${RESOURCES_DIR}/relay-bundle"
        cp "$relay_src" "${RESOURCES_DIR}/relay-bundle/isoform-relay"
        chmod +x "${RESOURCES_DIR}/relay-bundle/isoform-relay"
    fi

    # cua-driver — only embed when we actually have an arm64 staged copy.
    # The download script no-ops on amd64 / Windows / Linux so the bundle
    # dir simply won't exist on those targets.
    if [ "$EFFECTIVE_TARGET_ARCH" = arm64 ] && bundle_macho_matches "$CUA_DRIVER_BUNDLE/CuaDriver.app/Contents/MacOS/cua-driver" "$EFFECTIVE_TARGET_ARCH"; then
        echo "[build-wails] Embedding cua-driver bundle into ${APP_NAME}..."
        rm -rf "${RESOURCES_DIR}/cua-driver-bundle"
        mkdir -p "${RESOURCES_DIR}/cua-driver-bundle"
        # ditto preserves xattrs and the nested .app's symlink structure;
        # cp -r can flatten symlinks and confuse codesign.
        ditto "$CUA_DRIVER_BUNDLE/CuaDriver.app" "${RESOURCES_DIR}/cua-driver-bundle/CuaDriver.app"
        # Copy the marker files too — they're useful for diagnose / version
        # debugging on a customer install.
        for marker in .version .target .bundle-id; do
            if [ -f "${CUA_DRIVER_BUNDLE}/${marker}" ]; then
                cp "${CUA_DRIVER_BUNDLE}/${marker}" "${RESOURCES_DIR}/cua-driver-bundle/${marker}"
            fi
        done
    else
        rm -rf "${RESOURCES_DIR}/cua-driver-bundle"
        echo "[build-wails] cua-driver bundle not staged, skipping cua-driver embed"
    fi

    cp "${WAILS_DIR}/build/darwin/iconfile.icns" "${WAILS_DIR}/build/iconfile.icns"
    cp "${WAILS_DIR}/build/iconfile.icns" "${RESOURCES_DIR}/iconfile.icns"
    echo "[build-wails] Replaced .app icon with Violet icon"

    # Bundle sherpa-onnx + onnxruntime dylibs into Contents/Frameworks/
    # before signing so the signature covers them.
    bundle_sherpa_dylibs_macos

    # Install the Quick Chat Spotlight helper as a nested background .app
    # so it ships with the .app bundle and gets covered by codesigning.
    install_quick_chat_spotlight
    install_darwin_ocr_helper
    install_darwin_pii_helper

    # Catch stale or wrong-architecture helpers and libraries before signing.
    while IFS= read -r -d '' nested; do
        if is_macho_binary "$nested" && ! bundle_macho_matches "$nested" "$EFFECTIVE_TARGET_ARCH"; then
            echo "[build-wails] ERROR: bundled Mach-O has the wrong architecture: $nested" >&2
            exit 1
        fi
    done < <(find "$APP_PATH/Contents" -type f -print0)
    echo "[build-wails] Verified bundled Mach-O architecture: ${EFFECTIVE_TARGET_ARCH}"

    # Code signing (macOS only).
    # When APPLE_SIGNING_IDENTITY is set, use Developer ID signing + notarization.
    # An isoform build without it is signed with the "Isoform Dev" identity when
    # it exists (dev-signing-identity.sh create), so its designated requirement,
    # and the privacy grants keyed to it, survive rebuilds.
    # Otherwise fall back to ad-hoc signing.
    if [ "$(uname -s)" = "Darwin" ] && command -v codesign &>/dev/null; then
        ENTITLEMENTS="${WAILS_DIR}/build/darwin/entitlements.plist"
        DEV_SIGNING_IDENTITY=""
        DEV_SIGNING_KEYCHAIN=""
        if [ -z "${APPLE_SIGNING_IDENTITY:-}" ] && [ "$FLAVOR" = "isoform" ]; then
            if dev_identity="$(bash "${SCRIPT_DIR}/dev-signing-identity.sh" find)"; then
                DEV_SIGNING_IDENTITY="$(printf '%s\n' "$dev_identity" | sed -n 1p)"
                DEV_SIGNING_KEYCHAIN="$(printf '%s\n' "$dev_identity" | sed -n 2p)"
            fi
        fi
        if [ -n "${APPLE_SIGNING_IDENTITY:-}" ]; then
            echo "[build-wails] Signing with Developer ID: ${APPLE_SIGNING_IDENTITY}"
            CODESIGN_IDENTITY="$APPLE_SIGNING_IDENTITY"
            CODESIGN_LABEL="Developer ID"
            CODESIGN_OPTIONS=(--timestamp --options runtime)

            # Re-sign the embedded CuaDriver.app first (with its own
            # AppleEvents entitlement). sign_macos_embedded_code skips
            # files inside it on the strength of this prior pass.
            sign_cua_driver_nested
            sign_spotlight_nested

            # Sign nested Mach-O code before the top-level bundle. The runtime
            # bundles live under Contents/Resources, so we cannot rely on
            # `codesign --deep` to discover and sign them correctly.
            sign_macos_embedded_code

            # Sign the main app bundle with entitlements (audio-input for microphone)
            codesign --force "${CODESIGN_OPTIONS[@]}" --entitlements "$ENTITLEMENTS" --sign "$CODESIGN_IDENTITY" "$APP_PATH"
            codesign --verify --deep --strict --verbose=4 "$APP_PATH"
            echo "[build-wails] Signed ${APP_NAME} with Developer ID"
            if [ "$FLAVOR" = "isoform" ]; then
                # Whatever gets granted to this Developer ID build is keyed
                # to its requirement, not the development identity's, so the
                # identity record is dropped and the next identity-signed
                # build clears once.
                bash "${SCRIPT_DIR}/reset-dev-permissions.sh" --forget || true
            fi

            # Notarize if Apple ID credentials are available
            if notary_credentials_available; then
                echo "[build-wails] Submitting ${APP_NAME} for notarization..."
                NOTARIZE_ZIP=$(mktemp -t yansu-notarize.XXXXXX).zip
                ditto -c -k --keepParent "$APP_PATH" "$NOTARIZE_ZIP"

                NOTARY_ID=""
                if ! notarize_with_retry "$NOTARIZE_ZIP" "$APP_NAME"; then
                    if [ -n "$NOTARY_ID" ]; then
                        echo "[build-wails] Fetching notary log for submission ${NOTARY_ID}..." >&2
                        notarytool_with_credentials log "$NOTARY_ID" || true
                    fi
                    rm -f "$NOTARIZE_ZIP"
                    exit 1
                fi

                rm -f "$NOTARIZE_ZIP"

                # Staple the notarization ticket
                xcrun stapler staple "$APP_PATH"
                echo "[build-wails] Notarization complete, ticket stapled"
            else
                echo "[build-wails] Notarization credentials unavailable; stopping." >&2
                exit 1
            fi
        elif [ -n "$DEV_SIGNING_IDENTITY" ]; then
            echo "[build-wails] Signing with the development identity Isoform Dev (${DEV_SIGNING_IDENTITY}, ${DEV_SIGNING_KEYCHAIN})"
            CODESIGN_IDENTITY="$DEV_SIGNING_IDENTITY"
            CODESIGN_LABEL="the development identity Isoform Dev"
            # No hardened runtime and no timestamp: a development build keeps
            # the ad-hoc build's runtime behaviour (the bundled dylibs and
            # helpers load as before) and is never notarized. The identity
            # only makes the designated requirement stable. The keychain is
            # not in the search list, so codesign is pointed at it.
            CODESIGN_OPTIONS=(--timestamp=none --keychain "$DEV_SIGNING_KEYCHAIN")

            # Same inside-out order and entitlements as the Developer ID path,
            # so every nested helper carries the same identity as the bundle.
            sign_cua_driver_nested
            sign_spotlight_nested
            sign_macos_embedded_code
            codesign --force "${CODESIGN_OPTIONS[@]}" --entitlements "$ENTITLEMENTS" --sign "$CODESIGN_IDENTITY" "$APP_PATH"
            codesign --verify --deep --strict --verbose=4 "$APP_PATH"
            echo "[build-wails] Signed ${APP_NAME} with ${CODESIGN_LABEL}"
            echo "[build-wails] Designated requirement: $(designated_requirement "$APP_PATH")"
            echo "[build-wails] It is the same for every build signed with this identity, so the macOS privacy grants keyed to it survive rebuilds"

            # The grants are cleared only when they were made for a different
            # signature (the first identity-signed build after ad-hoc builds,
            # or a new certificate); reset-dev-permissions.sh keeps the record.
            if [ "$KEEP_PERMISSIONS" = true ]; then
                echo "[build-wails] Privacy grants left in place (--keep-permissions)"
            elif ! bash "${SCRIPT_DIR}/reset-dev-permissions.sh" --if-signature-changed com.wails.Isoform "$APP_PATH"; then
                echo "[build-wails] Warning: could not check or clear the previous build's privacy grants; remove Isoform from Screen & System Audio Recording and Accessibility in System Settings and add it again" >&2
            fi
        else
            codesign --deep --force --sign - "$APP_PATH"
            # Deep ad-hoc signing may discard nested runtime flags. Restore
            # the trusted CLI, then seal the app without re-signing children.
            if [ -f "$APP_PATH/Contents/Resources/yansu-cli-bundle/bin/yansu" ]; then
                codesign --force --options runtime --sign - "$APP_PATH/Contents/Resources/yansu-cli-bundle/bin/yansu"
            fi
            codesign --force --sign - "$APP_PATH"
            echo "[build-wails] Ad-hoc signed ${APP_NAME}"
            # An ad-hoc signature is keyed to this binary's cdhash, so the
            # privacy grants of the previous build no longer apply to it:
            # System Settings would keep showing switches that are on but
            # ignored. Clear them for the development identity so the app
            # asks again once. Release identities are never touched.
            if [ "$FLAVOR" = "isoform" ]; then
                echo "[build-wails] No Isoform Dev signing identity: this ad-hoc signature is keyed to the new binary's cdhash ($(designated_requirement "$APP_PATH")), so the previous build's macOS privacy grants (Screen Recording, Accessibility, Microphone, ...) no longer apply to it"
                if [ "$KEEP_PERMISSIONS" = true ]; then
                    # The grants stay, but whatever gets granted to this build
                    # is keyed to its cdhash, so the identity record is dropped
                    # and the next identity-signed build clears once.
                    echo "[build-wails] Privacy grants left in place (--keep-permissions); the app is denied until they are granted again"
                    bash "${SCRIPT_DIR}/reset-dev-permissions.sh" --forget || true
                else
                    echo "[build-wails] Clearing them so the app asks once"
                    # The new bundle is passed so it gets registered with Launch
                    # Services first; tccutil cannot resolve a bundle id whose
                    # .app was just replaced and never opened. A plain clear
                    # also drops the identity record.
                    if ! bash "${SCRIPT_DIR}/reset-dev-permissions.sh" com.wails.Isoform "$APP_PATH"; then
                        echo "[build-wails] Warning: could not clear the previous build's privacy grants; remove Isoform from Screen & System Audio Recording and Accessibility in System Settings and add it again" >&2
                    fi
                fi
                echo "[build-wails] Hint: run wails-gui/scripts/dev-signing-identity.sh create once; builds signed with that identity keep their grants across rebuilds"
            fi
        fi
    fi
    TOTAL_SIZE=$(du -sh "$APP_PATH" | cut -f1)
    echo "[build-wails] Done. App size: ${TOTAL_SIZE}"

    # Clear macOS quarantine attributes (only relevant on macOS).
    if command -v xattr &>/dev/null; then
        xattr -cr "$APP_PATH" || true
        echo "[build-wails] Cleared quarantine attributes"
    fi

    # Install only after embedding, signing, notarization and quarantine
    # cleanup. The installed bundle is the preferred Launch Services target.
    LAUNCH_APP_PATH="$APP_PATH"
    if [ "$INSTALL_APP" = true ]; then
        install_built_app
        LAUNCH_APP_PATH="${HOME}/Applications/${APP_NAME}"
    fi

    # Every Isoform.app ever opened stays registered with Launch Services
    # under com.wails.Isoform, and macOS reopens the app by bundle id (Screen
    # Recording's Quit & Reopen), so it could open an old worktree's build or
    # a deleted scratch copy. Register this build and unregister the other
    # build outputs and temporary copies; /Applications is never touched.
    if [ "$FLAVOR" = "isoform" ]; then
        bash "${SCRIPT_DIR}/unregister-stale-dev-builds.sh" com.wails.Isoform "$LAUNCH_APP_PATH" \
            || echo "[build-wails] Warning: could not clean up the other registered Isoform builds; macOS may reopen an old copy after Screen Recording's Quit & Reopen" >&2
    fi

    # Normalize macOS architecture to the same display values used by the
    # self-updater (version.go displayArch): arm64 stays as-is; amd64/x86_64
    # become x64. Filenames produced here must align with what the updater
    # looks for, otherwise GitHub fallback (suffix match on
    # -darwin-<arch>.app.tar.gz) cannot find the asset.
    #
    # Use the build target arch — not the runner's `uname -m` — so that
    # cross-builds (e.g. building darwin/amd64 on an Apple Silicon CI runner)
    # produce correctly named artifacts. Source of truth: --platform if given,
    # then GOARCH, then host arch.
    if [ -n "$PLATFORM" ]; then
        DARWIN_ARCH="${PLATFORM##*/}"
    elif [ -n "${GOARCH:-}" ]; then
        DARWIN_ARCH="$GOARCH"
    else
        DARWIN_ARCH=$(uname -m)
    fi
    case "$DARWIN_ARCH" in
        amd64|x86_64) DARWIN_ARCH=x64 ;;
        aarch64)      DARWIN_ARCH=arm64 ;;
    esac

    # Compose the versioned artifact stem once; falls back to an unversioned
    # name when no version is known (local dev builds without --version and
    # without a productVersion in wails.json).
    ARTIFACT_PREFIX="Isoform"
    if [ -n "$VERSION" ]; then
        ARTIFACT_STEM="${ARTIFACT_PREFIX}-${VERSION}-darwin-${DARWIN_ARCH}"
    else
        echo "[build-wails] Warning: no version detected, using unversioned artifact names" >&2
        ARTIFACT_STEM="${ARTIFACT_PREFIX}-darwin-${DARWIN_ARCH}"
    fi

    if [ "$CREATE_DMG" = true ] && [ "$(uname -s)" = "Darwin" ]; then
        DMG_PATH="${WAILS_DIR}/build/bin/${ARTIFACT_STEM}.dmg"
        rm -f "$DMG_PATH"

        # Try create-dmg first (pretty Finder layout); fall back to plain hdiutil.
        if command -v create-dmg &>/dev/null; then
            # The volume icon is the app's own icon.
            VOLICON_ARGS=()
            if [ -f "${APP_PATH}/Contents/Resources/iconfile.icns" ]; then
                VOLICON_ARGS+=(--volicon "${APP_PATH}/Contents/Resources/iconfile.icns")
            fi

            # Installer window (docs/handoff-2026-10-28/handoff-2026-10-28-1320-dmg-v1.0.0.md):
            # a 600x400 pt violet image, sharp on Retina through one TIFF made
            # from the 1x and 2x PNGs. --window-size is the outer window, so the
            # height adds the title bar: 32 pt on current macOS (measured on a
            # mounted build; the spec's 28 cut the bottom band by 4 pt).
            DMG_ASSETS="${WAILS_DIR}/build/darwin/dmg"
            DMG_BACKGROUND_DIR=$(mktemp -d)
            DMG_BACKGROUND_ARGS=()
            if tiffutil -cathidpicheck "${DMG_ASSETS}/background.png" "${DMG_ASSETS}/background@2x.png" \
                -out "${DMG_BACKGROUND_DIR}/background.tiff" >/dev/null; then
                DMG_BACKGROUND_ARGS+=(--background "${DMG_BACKGROUND_DIR}/background.tiff")
            else
                echo "[build-wails] Warning: could not make the DMG background TIFF" >&2
            fi

            # create-dmg may exit non-zero (e.g. exit 2 for Finder layout warnings,
            # or -1743 when terminal lacks Automation permission for Finder).
            # Suppress set -e and check the output file instead.
            create-dmg \
                --volname "${APP_NAME%.app}" \
                "${VOLICON_ARGS[@]+"${VOLICON_ARGS[@]}"}" \
                "${DMG_BACKGROUND_ARGS[@]+"${DMG_BACKGROUND_ARGS[@]}"}" \
                --window-pos 200 120 \
                --window-size 600 432 \
                --icon-size 128 \
                --text-size 13 \
                --icon "$APP_NAME" 170 190 \
                --hide-extension "$APP_NAME" \
                --app-drop-link 430 190 \
                "$DMG_PATH" \
                "$APP_PATH" || true
            rm -rf "$DMG_BACKGROUND_DIR"
        fi

        # Fallback to hdiutil if create-dmg is missing or failed to produce the DMG.
        if [ ! -f "$DMG_PATH" ]; then
            if command -v create-dmg &>/dev/null; then
                echo "[build-wails] create-dmg failed (AppleScript permission?), falling back to hdiutil" >&2
            else
                echo "[build-wails] create-dmg not found, falling back to hdiutil" >&2
                echo "[build-wails]   Install via: brew install create-dmg" >&2
            fi
            TMP_DMG_DIR=$(mktemp -d)
            cp -R "$APP_PATH" "$TMP_DMG_DIR/"
            ln -s /Applications "$TMP_DMG_DIR/Applications"
            hdiutil create -volname "${APP_NAME%.app}" -srcfolder "$TMP_DMG_DIR" -ov -format UDZO -o "${DMG_PATH%.dmg}"
            rm -rf "$TMP_DMG_DIR"
            echo "[build-wails] Created $(basename "$DMG_PATH") (basic, no custom layout)"
        else
            echo "[build-wails] Created $(basename "$DMG_PATH") with create-dmg"
        fi

        # Sign and notarize the DMG if Developer ID is available
        if [ -f "$DMG_PATH" ] && [ -n "${APPLE_SIGNING_IDENTITY:-}" ]; then
            codesign --force --timestamp --sign "$APPLE_SIGNING_IDENTITY" "$DMG_PATH"
            echo "[build-wails] Signed DMG with Developer ID"

            if notary_credentials_available; then
                echo "[build-wails] Submitting DMG for notarization..."
                NOTARY_ID=""
                if ! notarize_with_retry "$DMG_PATH" "DMG"; then
                    if [ -n "$NOTARY_ID" ]; then
                        echo "[build-wails] Fetching notary log for submission ${NOTARY_ID}..." >&2
                        notarytool_with_credentials log "$NOTARY_ID" || true
                    fi
                    exit 1
                fi
                xcrun stapler staple "$DMG_PATH"
                echo "[build-wails] DMG notarization complete, ticket stapled"
            fi
        fi

        if [ -f "$DMG_PATH" ] && command -v xattr &>/dev/null; then
            xattr -cr "$DMG_PATH"
        fi
    fi

    # Create a .app.tar.gz for the self-updater. This preserves the full
    # signed+notarized bundle so updates don't break Gatekeeper.
    APP_TAR_GZ="${WAILS_DIR}/build/bin/${ARTIFACT_STEM}.app.tar.gz"
    tar -czf "$APP_TAR_GZ" -C "$(dirname "$APP_PATH")" "$(basename "$APP_PATH")"
    echo "[build-wails] Created self-update archive: $(basename "$APP_TAR_GZ") ($(du -sh "$APP_TAR_GZ" | cut -f1))"
else
    # Windows/Linux: plain binary in build/bin/
    BIN_DIR="${WAILS_DIR}/build/bin"
    EXE_NAME="Yansu"
    if [ -f "${BIN_DIR}/${EXE_NAME}.exe" ] || [ -f "${BIN_DIR}/${EXE_NAME}" ]; then
        if [ "$EFFECTIVE_TARGET_OS" = "windows" ]; then
            bundle_sherpa_dlls_windows "$BIN_DIR"
        elif [ "$EFFECTIVE_TARGET_OS" = "linux" ]; then
            bundle_sherpa_libs_linux "$BIN_DIR"
        fi

        # Copy the Quick Chat Spotlight helper next to Yansu.exe so
        # spotlightSupervisor can spawn it from the executable directory.
        install_quick_chat_spotlight

        if [ -d "$CLI_BUNDLE" ]; then
            echo "[build-wails] Copying bundled yansu CLI next to executable..."
            rm -rf "${BIN_DIR}/yansu-cli-bundle"
            cp -r "$CLI_BUNDLE" "${BIN_DIR}/yansu-cli-bundle"
        else
            echo "[build-wails] Warning: bundled yansu CLI not found, skipping CLI copy"
        fi

        if [ -d "$GIT_BUNDLE" ]; then
            echo "[build-wails] Copying git bundle next to executable..."
            rm -rf "${BIN_DIR}/git-bundle"
            # Preserve git-core symlinks; macOS cp -r can expand them into full files.
            cp -RP "$GIT_BUNDLE" "${BIN_DIR}/git-bundle"
            find "${BIN_DIR}/git-bundle" -type l ! -exec test -e {} \; -delete 2>/dev/null || true
        else
            echo "[build-wails] Warning: git bundle not found, skipping git copy"
        fi

        if [ -d "$BUN_BUNDLE" ]; then
            echo "[build-wails] Copying bun bundle next to executable..."
            rm -rf "${BIN_DIR}/bun-bundle"
            cp -r "$BUN_BUNDLE" "${BIN_DIR}/bun-bundle"
        else
            echo "[build-wails] Warning: bun bundle not found, skipping bun copy"
        fi

        if [ -d "$FFMPEG_BUNDLE" ]; then
            echo "[build-wails] Copying ffmpeg bundle next to executable..."
            rm -rf "${BIN_DIR}/ffmpeg-bundle"
            cp -r "$FFMPEG_BUNDLE" "${BIN_DIR}/ffmpeg-bundle"
        else
            echo "[build-wails] Warning: ffmpeg bundle not found, skipping ffmpeg copy"
        fi

        echo "[build-wails] Copying pinned OpenCLI bundle next to executable..."
        rm -rf "${BIN_DIR}/opencli-bundle"
        cp -R "$OPENCLI_BUNDLE" "${BIN_DIR}/opencli-bundle"

        # PaddleOCR bundle is windows/amd64 only — Yansu.exe at runtime resolves
        # it via locatePaddleExe() in internal/platform/ocr/ocr_windows_paddle.go,
        # which checks <exe-dir>/paddleocr/PaddleOCR-json.exe.
        if [ "$EFFECTIVE_TARGET_OS" = "windows" ]; then
            if [ -f "${PADDLEOCR_BUNDLE}/PaddleOCR-json.exe" ]; then
                echo "[build-wails] Copying PaddleOCR bundle next to executable..."
                rm -rf "${BIN_DIR}/paddleocr"
                cp -r "$PADDLEOCR_BUNDLE" "${BIN_DIR}/paddleocr"
            else
                echo "[build-wails] Warning: PaddleOCR bundle not found, Chinese OCR will fall back to Windows.Media.Ocr"
            fi
        fi

        TOTAL_SIZE=$(du -sh "$BIN_DIR" | cut -f1)
        echo "[build-wails] Done. Output size: ${TOTAL_SIZE}"
    else
        echo "[build-wails] Warning: no executable found in ${BIN_DIR}, skipping bundle copy"
    fi
fi
