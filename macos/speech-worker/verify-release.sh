#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)

fail() {
    echo "verify-release: $*" >&2
    exit 1
}

if [[ "${1:-}" == "--app-only" ]]; then
    (( $# == 2 )) || { echo "usage: $0 --app-only /path/to/app" >&2; exit 64; }
    app=$2
    [[ "$app" == /* ]] || app=$(cd -- "$app" && pwd)
    [[ -d "$app" ]] || fail "app bundle is missing"
    verify_dmg=0
elif (( $# == 1 )); then
    dmg=$1
    [[ "$dmg" == /* ]] || dmg=$(cd -- "$(dirname -- "$dmg")" && pwd)/$(basename -- "$dmg")
    [[ -f "$dmg" ]] || fail "DMG is missing"
    command -v hdiutil >/dev/null || fail "hdiutil is required"
    mkdir -p "$repo_root/.build"
    hdiutil verify "$dmg"
    mountpoint=$(mktemp -d "$repo_root/.build/verify-mount.XXXXXX")
    mounted=0
    cleanup() {
        if (( mounted == 1 )); then hdiutil detach "$mountpoint" -force >/dev/null 2>&1 || true; fi
        rmdir "$mountpoint" 2>/dev/null || true
    }
    trap cleanup EXIT
    hdiutil attach "$dmg" -readonly -nobrowse -mountpoint "$mountpoint" >/dev/null
    mounted=1
    app=$(find "$mountpoint" -type d -name '*.app' -print -quit)
    [[ -n "$app" ]] || fail "DMG does not contain an app bundle"
    verify_dmg=1
else
    echo "usage: $0 --app-only /path/to/app | /path/to/dmg" >&2
    exit 64
fi

command -v codesign >/dev/null || fail "codesign is required"
command -v file >/dev/null || fail "file is required"
command -v lipo >/dev/null || fail "lipo is required"
command -v plutil >/dev/null || fail "plutil is required"
command -v python3 >/dev/null || fail "python3 is required"
command -v realpath >/dev/null || fail "realpath is required"
[[ -f "$app/Contents/MacOS/DoublanguSpeechWorker" ]] || fail "app executable is missing"
[[ -f "$app/Contents/Info.plist" ]] || fail "Info.plist is missing"
[[ -f "$app/Contents/Resources/receipt/release-manifest.json" ]] || fail "release manifest is missing"
plutil -lint "$app/Contents/Info.plist" >/dev/null
[[ "$(plutil -extract CFBundleIdentifier raw -o - "$app/Contents/Info.plist")" == "io.evren.doublangu.speech-worker" ]] || fail "bundle id mismatch"
[[ "$(plutil -extract LSUIElement raw -o - "$app/Contents/Info.plist")" == "true" ]] || fail "LSUIElement is not enabled"

python3 - "$app/Contents/Resources/receipt/release-manifest.json" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
payload = json.loads(path.read_text(encoding="utf-8"))
required = {
    "schema_version", "app_version", "bundle_id", "application_commit", "python_version",
    "mlx_audio_version", "model_repository", "model_revision", "tokenizer_repository",
    "tokenizer_revision", "reference_audio_sha256", "runtime_receipt", "minimum_macos",
    "architecture", "built_at_utc",
}
if set(payload) != required:
    raise SystemExit("release manifest fields are not exact")
if payload["schema_version"] != 1 or payload["bundle_id"] != "io.evren.doublangu.speech-worker":
    raise SystemExit("release manifest identity is invalid")
if payload["architecture"] != "arm64" or payload["minimum_macos"] != "14.0":
    raise SystemExit("release platform identity is invalid")
if payload["model_revision"] != "03565773edd72e949572557597af8063bb49a18a":
    raise SystemExit("model revision is invalid")
if payload["tokenizer_revision"] != "e0c9886f0e1c35ae85b1f27277416fb1":
    raise SystemExit("tokenizer revision is invalid")
receipt = payload["runtime_receipt"]
if not isinstance(receipt, str) or len(receipt) != 71 or not receipt.startswith("sha256:"):
    raise SystemExit("runtime receipt is invalid")
PY

runtime="$app/Contents/Resources/runtime"
runtime_python="$runtime/venv/bin/python"
[[ -d "$runtime" ]] || fail "bundled runtime is missing"
[[ -f "$runtime/receipt/runtime.json" ]] || fail "runtime receipt is missing"
[[ -f "$runtime/receipt/uv.lock" ]] || fail "bundled runtime lockfile is missing"
[[ -f "$runtime/server.py" && -f "$runtime/prepare_model.py" ]] || fail "runtime sources are missing"
[[ -x "$runtime_python" ]] || fail "bundled Python executable is missing"
python3 - "$runtime/receipt/runtime.json" "$runtime/receipt/uv.lock" \
    "$app/Contents/Resources/receipt/release-manifest.json" <<'PY'
import hashlib
import json
import sys
from pathlib import Path

runtime_receipt, lockfile, manifest = map(Path, sys.argv[1:])
runtime = json.loads(runtime_receipt.read_text(encoding="utf-8"))
if set(runtime) != {
    "architecture", "built_at_utc", "mlx_audio_version", "mlx_version", "python_version",
    "runtime_receipt", "schema_version", "uv_lock_sha256",
}:
    raise SystemExit("runtime receipt fields are not exact")
if runtime["schema_version"] != 1 or runtime["architecture"] != "arm64":
    raise SystemExit("runtime platform identity is invalid")
if runtime["python_version"] != "3.12.11" or runtime["mlx_audio_version"] != "0.4.7" or runtime["mlx_version"] != "0.32.2":
    raise SystemExit("runtime dependency identity is invalid")
lock_sha256 = hashlib.sha256(lockfile.read_bytes()).hexdigest()
if runtime["uv_lock_sha256"] != lock_sha256 or runtime["runtime_receipt"] != "sha256:" + lock_sha256:
    raise SystemExit("runtime lock receipt does not match the bundled lockfile")
if json.loads(manifest.read_text(encoding="utf-8"))["runtime_receipt"] != runtime["runtime_receipt"]:
    raise SystemExit("release and runtime receipts do not match")
PY
PYTHONNOUSERSITE=1 PYTHONDONTWRITEBYTECODE=1 \
    PYTHONPATH="$runtime/venv/lib/python3.12/site-packages" \
    "$runtime_python" -c 'import platform, mlx, mlx_audio, numpy; assert platform.machine() == "arm64"; assert platform.python_version() == "3.12.11"'

codesign --verify --deep --strict --verbose=2 "$app"
while IFS= read -r -d '' candidate; do
    if [[ "$(file -b "$candidate")" == *Mach-O* ]]; then
        codesign --verify --strict --verbose=2 "$candidate"
        [[ "$(lipo -archs "$candidate" 2>/dev/null)" == "arm64" ]] || fail "non-arm64 Mach-O: $candidate"
    fi
done < <(find "$app/Contents" -type f -print0)

if find "$app" -type f \( -name '*.safetensors' -o -name '*.ckpt' -o \
    -name '*.pth' ! -name '_virtualenv.pth' -o -name '*.m4a' -o -name '*.partial' -o \
    -name '*.ready' -o -name '.env' \) -print -quit | grep -q .; then
    fail "app contains model, generated audio, or secret material"
fi
if find "$app" -type f \( -name 'dutch-reference-v1.wav' -o -name 'voice_nl.flac' -o -name '*worker-token*' \) -print -quit | grep -q .; then
    fail "app contains private reference or token material"
fi
if rg -a -F -- "$repo_root" "$app" >/dev/null 2>&1; then
    fail "app contains an absolute build-checkout path"
fi

escaping_symlink=
while IFS= read -r -d '' link; do
    target=$(realpath "$link") || fail "could not resolve app symlink"
    case "$target" in
        "$app"/*) ;;
        *) escaping_symlink=1; break ;;
    esac
done < <(find "$app" -type l -print0)
[[ -z "$escaping_symlink" ]] || fail "app contains an escaping symlink"

if (( verify_dmg == 1 )); then
    hdiutil detach "$mountpoint" >/dev/null
    mounted=0
fi
echo "release verification passed: $app"
