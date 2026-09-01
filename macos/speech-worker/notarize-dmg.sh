#!/usr/bin/env bash
set -euo pipefail

if (( $# != 1 )); then
    echo "usage: $0 /absolute/path/to/release.dmg" >&2
    exit 64
fi
dmg=$1
[[ "$dmg" == /* && -f "$dmg" ]] || { echo "a release DMG absolute path is required" >&2; exit 64; }
[[ "$dmg" != *-dev.dmg ]] || { echo "development DMGs are not notarized" >&2; exit 64; }

codesign_identity=$(printenv CODESIGN_IDENTITY 2>/dev/null || true)
notary_profile=$(printenv NOTARY_PROFILE 2>/dev/null || true)
[[ -n "$codesign_identity" ]] || { echo "CODESIGN_IDENTITY is required" >&2; exit 64; }
[[ -n "$notary_profile" ]] || { echo "NOTARY_PROFILE is required" >&2; exit 64; }
command -v xcrun >/dev/null || { echo "xcrun is required" >&2; exit 1; }
command -v codesign >/dev/null || { echo "codesign is required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 is required" >&2; exit 1; }

codesign --verify --deep --strict --verbose=2 "$dmg"
notary_log="$dmg.notary.json"
xcrun notarytool submit "$dmg" --keychain-profile "$notary_profile" --wait --output-format json > "$notary_log"
submission_id=$(python3 - "$notary_log" <<'PY'
import json
import sys
from pathlib import Path

payload = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
value = payload.get("id")
if not isinstance(value, str) or not value:
    raise SystemExit("notarytool returned no submission id")
print(value)
PY
)
xcrun notarytool log "$submission_id" --keychain-profile "$notary_profile" > "$dmg.notary-detail.json"
xcrun stapler staple "$dmg"
xcrun stapler validate "$dmg"
spctl --assess --type open --context context:primary-signature --verbose=4 "$dmg"
shasum -a 256 "$dmg" > "$dmg.sha256"
echo "notarized DMG: $dmg"
