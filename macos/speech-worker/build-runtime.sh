#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(cd -- "$script_dir/../.." && pwd)
python_project="$script_dir/python"
build_root="$script_dir/.build"
stage="$build_root/runtime-staging.$$"
runtime_out="$build_root/runtime"
minimum_free_bytes=$((12 * 1024 * 1024 * 1024))

fail() {
    echo "build-runtime: $*" >&2
    exit 1
}

cleanup() {
    if [[ -n "${stage:-}" && -e "$stage" ]]; then
        rm -rf -- "$stage"
    fi
}
trap cleanup EXIT

[[ "$(uname -m)" == "arm64" ]] || fail "an arm64 build Mac is required"
command -v uv >/dev/null || fail "uv is required"
command -v shasum >/dev/null || fail "shasum is required"
command -v python3 >/dev/null || fail "python3 is required for receipt generation"
command -v rg >/dev/null || fail "rg is required"
command -v file >/dev/null || fail "file is required"
command -v lipo >/dev/null || fail "lipo is required"
command -v stat >/dev/null || fail "stat is required"

available_kib=$(df -P -k "$repo_root" | awk 'NR == 2 { print $4 }')
[[ "$available_kib" =~ ^[0-9]+$ ]] || fail "could not measure free storage"
(( available_kib * 1024 >= minimum_free_bytes )) || fail "at least 12 GiB free storage is required"
[[ -f "$python_project/pyproject.toml" ]] || fail "python project is missing"
[[ -f "$python_project/uv.lock" ]] || fail "python lockfile is missing"
uv lock --project "$python_project" --check

mkdir -p "$build_root"
uv python install 3.12.11
managed_python=$(uv python find 3.12.11)
[[ -x "$managed_python" ]] || fail "uv-managed CPython 3.12.11 was not found"
managed_root=$(cd -- "$(dirname -- "$managed_python")/.." && pwd)

mkdir -p "$stage/runtime/receipt"
ditto "$managed_root" "$stage/runtime/python"
runtime_python="$stage/runtime/python/bin/$(basename "$managed_python")"
[[ -x "$runtime_python" ]] || fail "staged CPython interpreter is missing"

uv venv --no-project --relocatable "$stage/runtime/venv" --python "$runtime_python"
venv_python="$stage/runtime/venv/bin/python"
[[ -x "$venv_python" ]] || fail "staged virtual environment is missing"

uv export --project "$python_project" --frozen --no-dev --no-emit-project \
    --no-header --no-annotate --format requirements.txt \
    --output-file "$stage/runtime/receipt/requirements.txt"
uv pip sync --python "$venv_python" --link-mode copy \
    "$stage/runtime/receipt/requirements.txt"

python3 - "$stage/runtime/venv/bin/python" "$runtime_python" <<'PY'
import os
import sys
from pathlib import Path

wrapper = Path(sys.argv[1])
interpreter = Path(sys.argv[2])
wrapper.unlink(missing_ok=True)
wrapper.symlink_to(os.path.relpath(interpreter, wrapper.parent))
PY
find "$stage/runtime/venv/bin" -mindepth 1 ! -path "$stage/runtime/venv/bin/python" -exec rm -rf -- {} +
rm -f -- "$stage/runtime/venv/pyvenv.cfg" "$stage/runtime/venv/.lock"

while IFS= read -r -d '' candidate; do
    if [[ "$(file -b "$candidate")" == *"Mach-O universal"* ]]; then
        thin_candidate="$candidate.arm64.$$"
        lipo -thin arm64 "$candidate" -output "$thin_candidate"
        chmod "$(stat -f '%Lp' "$candidate")" "$thin_candidate"
        mv -- "$thin_candidate" "$candidate"
    fi
done < <(find "$stage/runtime" -type f -print0)

cp "$python_project/uv.lock" "$stage/runtime/receipt/uv.lock"
cp "$python_project/server.py" "$stage/runtime/server.py"
cp "$python_project/prepare_model.py" "$stage/runtime/prepare_model.py"
chmod 755 "$stage/runtime/server.py" "$stage/runtime/prepare_model.py"

runtime_site_packages="$stage/runtime/venv/lib/python3.12/site-packages"
runtime_python() {
    PYTHONNOUSERSITE=1 PYTHONDONTWRITEBYTECODE=1 \
        PYTHONPATH="$runtime_site_packages" "$venv_python" "$@"
}

python_version=$(runtime_python -c 'import platform; print(platform.python_version())')
architecture=$(runtime_python -c 'import platform; print(platform.machine())')
[[ "$python_version" == "3.12.11" ]] || fail "staged runtime is $python_version, expected 3.12.11"
[[ "$architecture" == "arm64" ]] || fail "staged runtime architecture is $architecture"
runtime_python -c 'import mlx, mlx_audio, numpy'

lock_sha256=$(shasum -a 256 "$stage/runtime/receipt/uv.lock" | awk '{print $1}')
python3 - "$stage/runtime/receipt/runtime.json" "$python_version" "$architecture" "$lock_sha256" <<'PY'
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

output, python_version, architecture, lock_sha256 = sys.argv[1:]
payload = {
    "schema_version": 1,
    "python_version": python_version,
    "architecture": architecture,
    "mlx_audio_version": "0.4.7",
    "mlx_version": "0.32.2",
    "uv_lock_sha256": lock_sha256,
    "runtime_receipt": "sha256:" + lock_sha256,
    "built_at_utc": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
}
Path(output).write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")
PY

find "$stage/runtime" -type d -name '__pycache__' -prune -exec rm -rf {} +
find "$stage/runtime" -type f \( -name '*.pyc' -o -name '*.pyo' -o -name '*.safetensors' -o -name '*.ckpt' \) -delete
if find "$stage/runtime" -type f \( \
    -name '.env' -o -name '.env.*' -o -name '.netrc' -o \
    -name 'credentials.json' -o -name 'token.json' -o \
    -name 'id_rsa' -o -name 'id_ecdsa' -o -name 'id_ed25519' -o \
    -name '*.p12' -o -name '*.pfx' -o -name '*.key' \
\) -print -quit | grep -q .; then
    fail "runtime contains secret material"
fi
if rg -a -F -- "$repo_root" "$stage/runtime" >/dev/null 2>&1; then
    fail "runtime contains an absolute build-checkout path"
fi

escaping_symlink=
while IFS= read -r -d '' link; do
    target=$(realpath "$link") || fail "could not resolve runtime symlink"
    case "$target" in
        "$stage/runtime"/*) ;;
        *) escaping_symlink=1; break ;;
    esac
done < <(find "$stage/runtime" -type l -print0)
[[ -z "$escaping_symlink" ]] || fail "runtime contains an escaping symlink"

if [[ -e "$runtime_out" ]]; then
    rm -rf -- "$runtime_out"
fi
mv "$stage/runtime" "$runtime_out"
stage=

relocation_probe="$build_root/runtime-relocation-probe.$$"
cleanup_relocation() {
    if [[ -e "$relocation_probe" && ! -e "$runtime_out" ]]; then
        mv "$relocation_probe" "$runtime_out"
    fi
    if [[ -e "$relocation_probe" ]]; then
        rm -rf -- "$relocation_probe"
    fi
}
trap cleanup_relocation EXIT
mv "$runtime_out" "$relocation_probe"
PYTHONNOUSERSITE=1 PYTHONDONTWRITEBYTECODE=1 \
    PYTHONPATH="$relocation_probe/venv/lib/python3.12/site-packages" \
    "$relocation_probe/venv/bin/python" -c 'import mlx, mlx_audio, numpy'
mv "$relocation_probe" "$runtime_out"
trap - EXIT

echo "runtime ready: $runtime_out"
