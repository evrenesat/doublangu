#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
dist_dir="$repo_root/dist/macos"
version=$(tr -d '[:space:]' < "$script_dir/VERSION")
mode=development

for argument in "$@"; do
    case "$argument" in
        --development) mode=development ;;
        --release) mode=release ;;
        *) echo "usage: $0 [--development|--release]" >&2; exit 64 ;;
    esac
done

fail() {
    echo "build-dmg: $*" >&2
    exit 1
}

command -v hdiutil >/dev/null || fail "hdiutil is required"
command -v codesign >/dev/null || fail "codesign is required"

app_name="Doublangu Speech Worker.app"
app_dir="$dist_dir/$app_name"
[[ -d "$app_dir" ]] || fail "app is missing; run build-app.sh first"

if [[ "$mode" == "release" ]]; then
    codesign_identity=$(printenv CODESIGN_IDENTITY 2>/dev/null || true)
    [[ -n "$codesign_identity" ]] || fail "CODESIGN_IDENTITY is required for a release DMG"
    dmg_name="Doublangu-Speech-Worker-$version-arm64.dmg"
else
    codesign_identity=-
    dmg_name="Doublangu-Speech-Worker-$version-arm64-dev.dmg"
fi

staging="$repo_root/.build/doublangu-speech-worker-dmg-staging"
if [[ -e "$staging" ]]; then
    rm -rf -- "$staging"
fi
mkdir -p "$staging"
ditto "$app_dir" "$staging/$app_name"
ln -s /Applications "$staging/Applications"

output="$dist_dir/$dmg_name"
if [[ -e "$output" ]]; then
    rm -f -- "$output"
fi
hdiutil create -volname "Doublangu Speech Worker $version" -srcfolder "$staging" \
    -format UDZO -imagekey zlib-level=9 -ov "$output"
codesign --force --sign "$codesign_identity" "$output"
codesign --verify --strict --verbose=2 "$output"
hdiutil verify "$output"

rm -rf -- "$staging"
echo "dmg ready: $output"
