#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  printf 'Usage: %s OUTPUT_ARCHIVE\n' "$0" >&2
  exit 2
fi

readonly output_archive="$1"
project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly project_root
readonly build_version="${DOUBLANGU_BUILD_VERSION:-local}"

if [[ ! "$build_version" =~ ^[A-Za-z0-9._-]{1,64}$ ]]; then
  printf 'Error: DOUBLANGU_BUILD_VERSION must be a safe 1-64 character identifier.\n' >&2
  exit 1
fi

for command_name in go node npm tar; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'Error: required build command is unavailable: %s\n' "$command_name" >&2
    exit 1
  fi
done

node_major="$(node --version | sed -E 's/^v([0-9]+).*/\1/')"
if [[ ! "$node_major" =~ ^[0-9]+$ ]] || (( node_major < 24 )); then
  printf 'Error: Doublangu web builds require Node 24 or newer.\n' >&2
  exit 1
fi

build_root="$(mktemp -d)"
readonly build_root
cleanup() {
  rm -rf -- "$build_root"
}
trap cleanup EXIT

mkdir -p \
  "$build_root/release/bin" \
  "$build_root/release/contracts" \
  "$build_root/release/www"

(
  cd "$project_root"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
    go build -buildvcs=false -trimpath -ldflags='-s -w' \
    -o "$build_root/release/bin/doublangu-server" ./cmd/doublangu-server
)

DOUBLANGU_WEB_BASE_PATH='' DOUBLANGU_BUILD_VERSION="$build_version" \
  npm --prefix "$project_root/web" run build
cp -a "$project_root/web/build/." "$build_root/release/www/"
cp "$project_root/go.mod" "$build_root/release/go.mod"
cp \
  "$project_root/contracts/plugin-manifest-v1.schema.json" \
  "$build_root/release/contracts/plugin-manifest-v1.schema.json"
chmod 0755 "$build_root/release/bin/doublangu-server"

mkdir -p "$(dirname "$output_archive")"
tar \
  --sort=name \
  --mtime='@0' \
  --owner=0 \
  --group=0 \
  --numeric-owner \
  -C "$build_root/release" \
  -czf "$output_archive" \
  .
