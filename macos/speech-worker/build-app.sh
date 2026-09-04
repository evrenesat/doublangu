#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
package_dir="$script_dir/app"
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
    echo "build-app: $*" >&2
    exit 1
}

[[ "$(uname -m)" == "arm64" ]] || fail "an arm64 build Mac is required"
command -v swift >/dev/null || fail "Swift is required"
command -v codesign >/dev/null || fail "codesign is required"
command -v plutil >/dev/null || fail "plutil is required"
command -v strip >/dev/null || fail "strip is required"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "VERSION must be semantic"

if [[ "$mode" == "release" ]]; then
    codesign_identity=$(printenv CODESIGN_IDENTITY 2>/dev/null || true)
    [[ -n "$codesign_identity" ]] || fail "CODESIGN_IDENTITY is required for a release build"
    [[ -z "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]] || fail "release builds require a clean checkout"
else
    codesign_identity=-
fi

runtime_dir="$script_dir/.build/runtime"
if [[ ! -f "$runtime_dir/receipt/runtime.json" ]]; then
    "$script_dir/build-runtime.sh"
fi

swift test --package-path "$package_dir" --parallel
swift build --package-path "$package_dir" -c release --arch arm64

application_commit=unknown
if [[ -z "$(git -C "$repo_root" status --porcelain --untracked-files=all)" ]]; then
    application_commit=$(git -C "$repo_root" rev-parse HEAD)
fi
[[ "$application_commit" == unknown || "$application_commit" =~ ^[0-9a-f]{40}$ ]] || fail "invalid application revision"
build_number=$(git -C "$repo_root" rev-list --count HEAD)

app_name="Doublangu worker.app"
app_dir="$dist_dir/$app_name"
mkdir -p "$dist_dir"
if [[ -e "$app_dir" ]]; then
    rm -rf -- "$app_dir"
fi
mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources/receipt"
cp "$package_dir/.build/arm64-apple-macosx/release/DoublanguSpeechWorker" "$app_dir/Contents/MacOS/DoublanguSpeechWorker"
chmod 755 "$app_dir/Contents/MacOS/DoublanguSpeechWorker"
strip -S "$app_dir/Contents/MacOS/DoublanguSpeechWorker"
cp "$script_dir/python/uv.lock" "$app_dir/Contents/Resources/receipt/uv.lock"

runtime_receipt="sha256:$(shasum -a 256 "$script_dir/python/uv.lock" | awk '{print $1}')"
python_version="3.12.11"
[[ -f "$runtime_dir/receipt/runtime.json" ]] || fail "runtime receipt is missing"
ditto "$runtime_dir" "$app_dir/Contents/Resources/runtime"
python_version=$("$app_dir/Contents/Resources/runtime/venv/bin/python" -c 'import platform; print(platform.python_version())')
runtime_receipt=$(python3 - "$app_dir/Contents/Resources/runtime/receipt/runtime.json" <<'PY'
import json
import sys
from pathlib import Path

payload = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
if payload.get("schema_version") != 1 or payload.get("python_version") != "3.12.11" or payload.get("architecture") != "arm64":
    raise SystemExit("runtime receipt identity is invalid")
if payload.get("mlx_audio_version") != "0.4.7" or payload.get("mlx_version") != "0.32.2":
    raise SystemExit("runtime dependency versions are invalid")
receipt = payload.get("runtime_receipt", "")
if not receipt.startswith("sha256:") or len(receipt) != 71:
    raise SystemExit("runtime receipt digest is invalid")
print(receipt)
PY
)

python3 - "$app_dir/Contents/Resources/receipt/release-manifest.json" "$version" "$application_commit" "$python_version" "$runtime_receipt" <<'PY'
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

output, version, application_commit, python_version, runtime_receipt = sys.argv[1:]
payload = {
    "schema_version": 1,
    "app_version": version,
    "bundle_id": "io.evren.doublangu.speech-worker",
    "application_commit": application_commit,
    "python_version": python_version,
    "mlx_audio_version": "0.4.7",
    "model_repository": "mlx-community/chatterbox-multilingual-v3",
    "model_revision": "03565773edd72e949572557597af8063bb49a18a",
    "tokenizer_repository": "mlx-community/S3TokenizerV2",
    "tokenizer_revision": "e0c9886f0e1c35ae85b1f27277416fb1",
    "reference_audio_sha256": "1dd25cc2ea1aa8314af2ce2f062eb44beeb662482516177e098f58f6b6ce10f5",
    "runtime_receipt": runtime_receipt,
    "minimum_macos": "14.0",
    "architecture": "arm64",
    "built_at_utc": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
}
Path(output).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

sed -e "s/@VERSION@/$version/g" -e "s/@BUILD@/$build_number/g" \
    "$script_dir/Resources/Info.plist.in" > "$app_dir/Contents/Info.plist"
plutil -lint "$app_dir/Contents/Info.plist"

sign_target() {
    if [[ "$mode" == "release" ]]; then
        codesign --force --options runtime --timestamp --sign "$codesign_identity" "$1"
    else
        codesign --force --sign - "$1"
    fi
}

while IFS= read -r -d '' candidate; do
    if [[ "$(file -b "$candidate")" == *Mach-O* ]]; then
        sign_target "$candidate"
    fi
done < <(find "$app_dir/Contents" -type f -print0)

if [[ "$mode" == "release" ]]; then
    codesign --force --options runtime --timestamp --sign "$codesign_identity" \
        --entitlements "$script_dir/Resources/DoublanguSpeechWorker.entitlements" "$app_dir"
else
    codesign --force --sign - --entitlements "$script_dir/Resources/DoublanguSpeechWorker.entitlements" "$app_dir"
fi
codesign --verify --deep --strict --verbose=2 "$app_dir"
codesign -d --arch arm64 "$app_dir" >/dev/null 2>&1

echo "app ready: $app_dir"
