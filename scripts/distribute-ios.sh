#!/usr/bin/env bash
# Archives the Yansu iPhone app for App Store Connect, and exports or uploads an
# existing archive for internal TestFlight testing. This repository provisions no
# Apple team, signing identity, APNs key, or App Store Connect app record.
#
# Usage:
#   distribute-ios.sh verify                     unsigned Release compile; no team needed
#   distribute-ios.sh archive                    signed Release archive
#   distribute-ios.sh archive-upload             archive and upload in one command
#   distribute-ios.sh export <Yansu.xcarchive>   local App Store Connect package; uploads nothing
#   distribute-ios.sh upload <Yansu.xcarchive>   export and upload the same frozen archive
#
# Environment:
#   YANSU_IOS_TEAM_ID        your enrolled 10-character Apple team identifier (archive, export, upload)
#   YANSU_IOS_EXPORT_METHOD  must be app-store-connect (archive, export, upload)
#   YANSU_IOS_BUILD_NUMBER   numeric build number for archive (default: UTC timestamp)
#   ISOFORM_ASC_KEY_ID       App Store Connect API key ID for export/upload
#   ISOFORM_ASC_ISSUER_ID    App Store Connect API issuer ID for export/upload
#   ISOFORM_IOS_SIGNING_STYLE  manual to use App Store provisioning profiles
#   ISOFORM_IOS_APP_PROFILE    app profile name or UUID (optional)
#   ISOFORM_IOS_WIDGET_PROFILE widget profile name or UUID (optional)
#   ISOFORM_IOS_NOTIFICATION_PROFILE notification profile name or UUID (optional)
#   ISOFORM_IOS_CERTIFICATE    distribution certificate SHA-1 (optional)
#   ISOFORM_IOS_INTERNAL_ONLY  true (default) or false for App Store review
#   ISOFORM_IOS_EMBED_WATCH    YES to archive with the watch app inside (default NO; verify, archive)
# Set both API IDs to use ~/.appstoreconnect/private_keys/AuthKey_<KEY_ID>.p8;
# otherwise export/upload uses the Xcode account.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_ROOT="$REPO_ROOT/ios/.build-distribution"
ACTION="${1:-}"
TEAM_ID="${YANSU_IOS_TEAM_ID:-}"
EXPORT_METHOD="${YANSU_IOS_EXPORT_METHOD:-}"
INTERNAL_ONLY="${ISOFORM_IOS_INTERNAL_ONLY:-true}"
EMBED_WATCH="${ISOFORM_IOS_EMBED_WATCH:-NO}"
BUILD_NUMBER="${YANSU_IOS_BUILD_NUMBER:-}"
ARCHIVE_PATH="${2:-}"
ASC_KEY_ID="${ISOFORM_ASC_KEY_ID:-}"
ASC_ISSUER_ID="${ISOFORM_ASC_ISSUER_ID:-}"
PLIST_BUDDY=/usr/libexec/PlistBuddy
NOT_PROVISIONED="This repository does not provision an Apple team, signing identity, APNs key, or App Store Connect app record."

fail() {
    echo "$*" >&2
    exit 2
}

usage() {
    echo "Usage: $0 verify" >&2
    echo "       $0 archive|archive-upload" >&2
    echo "       $0 export|upload <existing-Yansu.xcarchive>" >&2
    echo "archive, export, and upload require YANSU_IOS_TEAM_ID (your enrolled Apple Developer team)" >&2
    echo "and YANSU_IOS_EXPORT_METHOD=app-store-connect. YANSU_IOS_BUILD_NUMBER optionally fixes the archive build number." >&2
    echo "$NOT_PROVISIONED" >&2
    exit 2
}

case "$ACTION" in
    verify|archive|archive-upload) [[ $# -eq 1 ]] || usage ;;
    export|upload) [[ $# -eq 2 ]] || usage ;;
    *) usage ;;
esac

if [[ -n "$TEAM_ID" && ! "$TEAM_ID" =~ ^[A-Z0-9]{10}$ ]]; then
    fail "YANSU_IOS_TEAM_ID must be a 10-character Apple team identifier."
fi
if [[ "$ACTION" != verify ]]; then
    # Signed actions refuse to run without an explicit team and export method.
    if [[ -z "$TEAM_ID" ]]; then
        fail "Refusing to run $ACTION: set YANSU_IOS_TEAM_ID to your enrolled Apple Developer team. $NOT_PROVISIONED"
    fi
    if [[ -z "$EXPORT_METHOD" ]]; then
        fail "Refusing to run $ACTION: set YANSU_IOS_EXPORT_METHOD=app-store-connect. It is the only export method this script supports."
    fi
    if [[ "$EXPORT_METHOD" != app-store-connect ]]; then
        fail "YANSU_IOS_EXPORT_METHOD must be app-store-connect. This script supports no other export method."
    fi
fi
case "$INTERNAL_ONLY" in
    true|false) ;;
    *) fail "ISOFORM_IOS_INTERNAL_ONLY must be true or false." ;;
esac
valid_build_number() {
    [[ "$1" =~ ^[1-9][0-9]{0,17}$ ]]
}
if [[ -n "$BUILD_NUMBER" ]] && ! valid_build_number "$BUILD_NUMBER"; then
    fail "YANSU_IOS_BUILD_NUMBER must contain 1 through 18 digits, with no leading zero."
fi

# Keep the archive frozen between steps and propagate its generated build number.
if [[ "$ACTION" == archive-upload ]]; then
    BUILD_NUMBER="${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}"
    valid_build_number "$BUILD_NUMBER" || fail "The generated build number is invalid."
    export YANSU_IOS_BUILD_NUMBER="$BUILD_NUMBER"
    "$0" archive
    exec "$0" upload "$OUTPUT_ROOT/archives/$BUILD_NUMBER/Yansu.xcarchive"
fi

command -v xcodebuild >/dev/null 2>&1 || fail "Install Xcode and complete its first-launch setup."
[[ -x "$PLIST_BUDDY" ]] || fail "This script requires macOS's PlistBuddy."
[[ ! -L "$OUTPUT_ROOT" ]] || fail "The distribution output directory must not be a symbolic link."
if [[ -e "$OUTPUT_ROOT" ]]; then
    [[ -d "$OUTPUT_ROOT" && "$(stat -f '%u' "$OUTPUT_ROOT")" == "$(id -u)" ]] ||
        fail "The distribution output directory must be owned by the current user."
fi

plist_value() {
    "$PLIST_BUDDY" -c "Print :$2" "$1" 2>/dev/null
}

inspect_archive() {
    [[ -d "$ARCHIVE_PATH" && "$ARCHIVE_PATH" == *.xcarchive ]] || fail "Select an existing .xcarchive."
    ARCHIVE_PATH="$(cd "$ARCHIVE_PATH" && pwd -P)"
    local archive_info="$ARCHIVE_PATH/Info.plist"
    local application_path app_info actual_build actual_version archived_team signed_team
    application_path="$(plist_value "$archive_info" ApplicationProperties:ApplicationPath)" || fail "The archive has no application metadata."
    [[ "$application_path" == Applications/Yansu.app ]] || fail "Select a Yansu app archive built from the Yansu scheme."
    ARCHIVED_APP="$ARCHIVE_PATH/Products/$application_path"
    app_info="$ARCHIVED_APP/Info.plist"
    [[ "$(plist_value "$app_info" CFBundleIdentifier)" == com.isoform.companion &&
       "$(plist_value "$archive_info" ApplicationProperties:CFBundleIdentifier)" == com.isoform.companion ]] ||
        fail "The frozen archive does not contain the checked-in bundle identifier com.isoform.companion."
    [[ "$(plist_value "$app_info" CFBundleSupportedPlatforms:0)" == iPhoneOS ]] ||
        fail "Select a real iPhone archive, not a simulator build."
    actual_build="$(plist_value "$app_info" CFBundleVersion)" || fail "The archived app has no build number."
    valid_build_number "$actual_build" || fail "The archived build number must be numeric."
    [[ "$(plist_value "$archive_info" ApplicationProperties:CFBundleVersion)" == "$actual_build" ]] ||
        fail "The archived app and archive metadata disagree about their build number."
    actual_version="$(plist_value "$app_info" CFBundleShortVersionString)" || fail "The archived app has no version."
    [[ "$(plist_value "$archive_info" ApplicationProperties:CFBundleShortVersionString)" == "$actual_version" ]] ||
        fail "The archived app and archive metadata disagree about their version."
    if [[ -n "$BUILD_NUMBER" && "$BUILD_NUMBER" != "$actual_build" ]]; then
        fail "YANSU_IOS_BUILD_NUMBER does not match the frozen archive; create a new archive instead of relabeling this binary."
    fi
    BUILD_NUMBER="$actual_build"
    /usr/bin/codesign --verify --strict "$ARCHIVED_APP" >/dev/null 2>&1 ||
        fail "The archive is not a valid signed device app. An unsigned verify archive cannot be exported or uploaded."
    signed_team="$(/usr/bin/codesign -d --verbose=4 "$ARCHIVED_APP" 2>&1 | sed -n 's/^TeamIdentifier=//p')"
    [[ "$signed_team" == "$TEAM_ID" ]] ||
        fail "YANSU_IOS_TEAM_ID does not match the frozen archive's signing team."
    # The code signature is authoritative. Xcode's archive metadata may omit
    # Team; when it is present, it must agree with the actual signing identity.
    archived_team="$(plist_value "$archive_info" ApplicationProperties:Team || true)"
    if [[ -n "$archived_team" && "$archived_team" != "$TEAM_ID" ]]; then
        fail "The archive's optional team metadata does not match its signing identity."
    fi
}

if [[ "$ACTION" == export || "$ACTION" == upload ]]; then
    AUTH_ARGS=()
    if [[ -n "$ASC_KEY_ID" || -n "$ASC_ISSUER_ID" ]]; then
        [[ -n "$ASC_KEY_ID" && -n "$ASC_ISSUER_ID" ]] ||
            fail "Set both ISOFORM_ASC_KEY_ID and ISOFORM_ASC_ISSUER_ID for API-key authentication."
        [[ "$ASC_KEY_ID" =~ ^[A-Za-z0-9]+$ ]] || fail "ISOFORM_ASC_KEY_ID must be alphanumeric."
        ASC_KEY_PATH="$HOME/.appstoreconnect/private_keys/AuthKey_${ASC_KEY_ID}.p8"
        [[ -f "$ASC_KEY_PATH" && -r "$ASC_KEY_PATH" ]] || fail "App Store Connect API key file is missing or unreadable."
        AUTH_ARGS=(-authenticationKeyPath "$ASC_KEY_PATH" -authenticationKeyID "$ASC_KEY_ID" -authenticationKeyIssuerID "$ASC_ISSUER_ID")
    fi
    # Read the existing binary before creating outputs or invoking distribution.
    # A changed environment cannot assign a new build number or team to it.
    inspect_archive
else
    BUILD_NUMBER="${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M%S)}"
    valid_build_number "$BUILD_NUMBER" || fail "The generated build number is invalid."
    if [[ "$ACTION" == verify ]]; then
        RUN_ROOT="$OUTPUT_ROOT/unsigned/$BUILD_NUMBER"
    else
        RUN_ROOT="$OUTPUT_ROOT/archives/$BUILD_NUMBER"
    fi
    [[ ! -e "$RUN_ROOT" && ! -L "$RUN_ROOT" ]] ||
        fail "That build already has an output directory. Use a new YANSU_IOS_BUILD_NUMBER; existing archives are never overwritten."
    ARCHIVE_PATH="$RUN_ROOT/Yansu.xcarchive"
fi

umask 077
mkdir -p "$OUTPUT_ROOT"
chmod 700 "$OUTPUT_ROOT"

if [[ "$ACTION" == verify || "$ACTION" == archive ]]; then
    mkdir -p "$(dirname "$RUN_ROOT")"
    mkdir "$RUN_ROOT"
    SIGNING_ARGS=(CODE_SIGNING_ALLOWED=NO CODE_SIGNING_REQUIRED=NO)
    if [[ "$ACTION" == archive ]]; then
        SIGNING_ARGS=(-allowProvisioningUpdates CODE_SIGNING_ALLOWED=YES "DEVELOPMENT_TEAM=$TEAM_ID" "CODE_SIGN_IDENTITY=Apple Development")
    fi
    # With the watch app inside, the YansuWithWatch scheme builds the watch targets first and
    # the embed setting lets them into the iPhone app. No -sdk then: it would force the iPhone
    # SDK onto the watch targets.
    case "$EMBED_WATCH" in
        NO) SCHEME_ARGS=(-scheme Yansu -sdk iphoneos) ;;
        YES) SCHEME_ARGS=(-scheme YansuWithWatch ISOFORM_EMBED_WATCH=YES) ;;
        *) fail "ISOFORM_IOS_EMBED_WATCH must be YES or NO." ;;
    esac
    echo "Preparing Release iPhone build $BUILD_NUMBER (watch app inside: $EMBED_WATCH): $ARCHIVE_PATH"
    xcodebuild \
        -project "$REPO_ROOT/ios/Yansu.xcodeproj" \
        "${SCHEME_ARGS[@]}" \
        -configuration Release \
        -destination 'generic/platform=iOS' \
        -derivedDataPath "$RUN_ROOT/DerivedData" \
        -archivePath "$ARCHIVE_PATH" \
        "CURRENT_PROJECT_VERSION=$BUILD_NUMBER" \
        "${SIGNING_ARGS[@]}" \
        archive
    if [[ "$ACTION" == verify ]]; then
        echo "Unsigned iPhone Release compilation passed. This archive cannot be installed, exported, or uploaded."
    else
        inspect_archive
        if [[ "$EMBED_WATCH" == YES ]]; then
            [[ -d "$ARCHIVED_APP/Watch/YansuWatch.app" ]] || fail "The archive was built with ISOFORM_IOS_EMBED_WATCH=YES but has no watch app inside."
        fi
        echo "Signed archive prepared. Export it with the same YANSU_IOS_TEAM_ID and YANSU_IOS_EXPORT_METHOD: $0 export '$ARCHIVE_PATH'"
    fi
    exit 0
fi

OPTIONS_DIR="$(mktemp -d "$OUTPUT_ROOT/export-options.XXXXXX")"
OPTIONS_PLIST="$OPTIONS_DIR/ExportOptions.plist"
cleanup() {
    local result=$?
    trap - EXIT
    rm -f "$OPTIONS_PLIST" || true
    rmdir "$OPTIONS_DIR" || true
    exit "$result"
}
trap cleanup EXIT
DESTINATION=export
if [[ "$ACTION" == upload ]]; then DESTINATION=upload; fi
cat > "$OPTIONS_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
    <key>method</key><string>$EXPORT_METHOD</string>
    <key>destination</key><string>$DESTINATION</string>
    <key>teamID</key><string>$TEAM_ID</string>
    <key>manageAppVersionAndBuildNumber</key><false/>
    <key>testFlightInternalTestingOnly</key><$INTERNAL_ONLY/>
    <key>uploadSymbols</key><true/>
</dict></plist>
EOF
# Manual export uses the same profile mapping as the successful local upload.
# Construct the plist with plistlib so profile names need no XML escaping.
if [[ "${ISOFORM_IOS_SIGNING_STYLE:-}" == manual || -n "${ISOFORM_IOS_APP_PROFILE:-}${ISOFORM_IOS_WIDGET_PROFILE:-}${ISOFORM_IOS_NOTIFICATION_PROFILE:-}${ISOFORM_IOS_CERTIFICATE:-}" ]]; then
    [[ -z "${ISOFORM_IOS_SIGNING_STYLE:-}" || "$ISOFORM_IOS_SIGNING_STYLE" == manual ]] || fail "Conflicting signing style and manual profile settings."
    python3 "$SCRIPT_DIR/ios-manual-profiles.py" "$OPTIONS_PLIST" "$TEAM_ID"
else
    [[ -z "${ISOFORM_IOS_SIGNING_STYLE:-}" || "$ISOFORM_IOS_SIGNING_STYLE" == automatic ]] || fail "ISOFORM_IOS_SIGNING_STYLE must be automatic or manual."
    "$PLIST_BUDDY" -c 'Add :signingStyle string automatic' "$OPTIONS_PLIST"
fi
chmod 600 "$OPTIONS_PLIST"
mkdir -p "$OUTPUT_ROOT/$ACTION"
EXPORT_PATH="$(mktemp -d "$OUTPUT_ROOT/$ACTION/$BUILD_NUMBER.XXXXXX")"
echo "$ACTION frozen build $BUILD_NUMBER (TestFlight internal-only: $INTERNAL_ONLY)."
xcodebuild \
    -exportArchive \
    -allowProvisioningUpdates \
    ${AUTH_ARGS[@]+"${AUTH_ARGS[@]}"} \
    -archivePath "$ARCHIVE_PATH" \
    -exportPath "$EXPORT_PATH" \
    -exportOptionsPlist "$OPTIONS_PLIST"
if [[ "$ACTION" == upload ]]; then
    echo "Upload command finished. Check App Store Connect processing before assigning the build to your internal tester group."
else
    echo "Distribution package exported locally: $EXPORT_PATH"
    echo "Nothing was uploaded. The package is for App Store Connect, not direct iPhone installation."
fi
