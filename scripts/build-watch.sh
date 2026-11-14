#!/usr/bin/env bash
# Builds the Isoform watch app for the watchOS Simulator and prints the path of YansuWatch.app.
#
# Environment (all optional):
#   YANSU_WATCH_CONFIGURATION  Debug (default) or Release
#   YANSU_WATCH_DESTINATION    xcodebuild destination (default: generic watchOS Simulator)
#   YANSU_WATCH_BUILD_DIR      derived data directory (default: ios/.build/watch)
#
# This script only builds; it boots no simulator. The watch app's logic is tested with the
# phone's unit tests (ios/YansuTests/WatchRecordingTests.swift).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CONFIGURATION="${YANSU_WATCH_CONFIGURATION:-Debug}"
DESTINATION="${YANSU_WATCH_DESTINATION:-generic/platform=watchOS Simulator}"
BUILD_DIR="${YANSU_WATCH_BUILD_DIR:-$REPO_ROOT/ios/.build/watch}"

if [[ $# -ne 0 ]]; then
    echo "Usage: $0" >&2
    exit 2
fi
case "$CONFIGURATION" in
    Debug | Release) ;;
    *)
        echo "YANSU_WATCH_CONFIGURATION must be Debug or Release." >&2
        exit 2
        ;;
esac
if ! command -v xcodebuild >/dev/null 2>&1; then
    echo "Install Xcode and select its developer directory before building the watch app." >&2
    exit 1
fi

xcodebuild \
    -quiet \
    -project "$REPO_ROOT/ios/Yansu.xcodeproj" \
    -scheme YansuWatch \
    -configuration "$CONFIGURATION" \
    -destination "$DESTINATION" \
    -derivedDataPath "$BUILD_DIR" \
    CODE_SIGNING_ALLOWED=YES \
    CODE_SIGN_IDENTITY=- \
    build

PRODUCTS_DIR="$BUILD_DIR/Build/Products/$CONFIGURATION-watchsimulator"
if [[ ! -d "$PRODUCTS_DIR/YansuWatch.app" ]]; then
    echo "The build finished, but YansuWatch.app was not found in $PRODUCTS_DIR." >&2
    exit 1
fi
echo "Built: $(cd "$PRODUCTS_DIR" && pwd)/YansuWatch.app"
