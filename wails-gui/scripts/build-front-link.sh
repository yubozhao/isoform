#!/bin/bash
# build-front-link.sh: build isoform-front-link (the computer's side of the
# Isoform front, ../../front) for the app to bundle. The toolchain is pinned
# by front/rust-toolchain.toml. Without cargo this warns and exits 0: the
# front stays unavailable in that build, nothing else changes.
#
#   build-front-link.sh [arm64|amd64]   # default: this machine's arch
#
# Output: wails-gui/front-link-bundle/isoform-front-link-darwin-<arch>, and
# for this machine's arch also front-link-bundle/isoform-front-link (the
# name the development app looks for).
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WAILS_DIR="$(dirname "$SCRIPT_DIR")"
FRONT_DIR="$(cd "$WAILS_DIR/.." && pwd)/front"
OUT_DIR="$WAILS_DIR/front-link-bundle"

case "$(uname -m)" in
    arm64|aarch64) HOST_ARCH=arm64 ;;
    x86_64) HOST_ARCH=amd64 ;;
    *) HOST_ARCH=unknown ;;
esac
ARCH="${1:-$HOST_ARCH}"
case "$ARCH" in
    arm64) TRIPLE=aarch64-apple-darwin ;;
    amd64) TRIPLE=x86_64-apple-darwin ;;
    *) echo "[front-link] unknown arch '$ARCH'; skipping" >&2; exit 0 ;;
esac

if [ "$(uname -s)" != Darwin ]; then
    echo "[front-link] macOS only for now; skipping" >&2
    exit 0
fi
if [ ! -f "$FRONT_DIR/Cargo.toml" ]; then
    echo "[front-link] $FRONT_DIR not found; skipping" >&2
    exit 0
fi

CARGO=""
for candidate in "$(command -v cargo 2>/dev/null || true)" "$HOME/.cargo/bin/cargo" /opt/homebrew/opt/rustup/bin/cargo; do
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then
        CARGO="$candidate"
        break
    fi
done
if [ -z "$CARGO" ]; then
    echo "[front-link] Warning: cargo not found; building without isoform-front-link (the Isoform front will be unavailable)" >&2
    exit 0
fi
export PATH="$(dirname "$CARGO"):$PATH"

TARGET_ARGS=()
BIN_DIR="$FRONT_DIR/target/release"
if [ "$ARCH" != "$HOST_ARCH" ]; then
    if ! rustup target list --installed 2>/dev/null | grep -qx "$TRIPLE"; then
        echo "[front-link] Warning: Rust target $TRIPLE is not installed (rustup target add $TRIPLE); skipping isoform-front-link for $ARCH" >&2
        exit 0
    fi
    TARGET_ARGS=(--target "$TRIPLE")
    BIN_DIR="$FRONT_DIR/target/$TRIPLE/release"
fi

# build-wails.sh exports MACOSX_DEPLOYMENT_TARGET=14.0 for cgo. rustc applies
# it to the host proc-macro dylibs too, and then cannot load them (E0463,
# "can't find crate for `zeroize_derive`"). Rust's own default (11.0 on arm64,
# 10.12 on x86_64) is below the app's 14.0 floor, so drop it for cargo.
build_link() {
    (cd "$FRONT_DIR" && unset MACOSX_DEPLOYMENT_TARGET && "$CARGO" build --release --locked --bin isoform-front-link ${TARGET_ARGS[@]+"${TARGET_ARGS[@]}"})
}
echo "[front-link] building isoform-front-link for $ARCH..."
# A build stopped midway (the app runner cancels stale builds) can leave a
# half-written proc-macro dylib that fails every later build ("mis-aligned
# LINKEDIT string pool", E0463). On failure, drop the release outputs and
# build once more from clean; a real compile error fails both times.
if ! build_link; then
    echo "[front-link] build failed; cleaning the release outputs and retrying once" >&2
    rm -rf "$FRONT_DIR/target/release" "$FRONT_DIR/target/$TRIPLE/release"
    build_link
fi
mkdir -p "$OUT_DIR"
cp "$BIN_DIR/isoform-front-link" "$OUT_DIR/isoform-front-link-darwin-$ARCH"
chmod +x "$OUT_DIR/isoform-front-link-darwin-$ARCH"
if [ "$ARCH" = "$HOST_ARCH" ]; then
    cp "$OUT_DIR/isoform-front-link-darwin-$ARCH" "$OUT_DIR/isoform-front-link"
fi
echo "[front-link] staged $OUT_DIR/isoform-front-link-darwin-$ARCH"
