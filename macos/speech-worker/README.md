# Doublangu Speech Worker

This is a native arm64 macOS 14+ menu-bar worker for the Doublangu Dutch
reader. It leases one job at a time from the beta HTTPS API, renders words and
short phrases with Xander, renders sentences through an on-demand bundled
Python/MLX-Audio child, and uploads authenticated mono 24 kHz AAC-LC M4A.

The app stores configuration and the crash-safe upload spool under:

`~/Library/Application Support/Doublangu Speech Worker/`

The model cache and reference WAV are private local state and are never bundled
or sent to the server. The Python child listens only on an app-selected
loopback port and receives no credentials.

## Development

The source-only app can be built without downloading a model:

```sh
xcrun swift-format lint --recursive app/Sources app/Tests
swift test --package-path app --parallel
swift build --package-path app -c release
./build-runtime.sh
./build-app.sh --development
./verify-release.sh --app-only ../../dist/macos/Doublangu\ Speech\ Worker.app
./build-dmg.sh --development
./verify-release.sh ../../dist/macos/Doublangu-Speech-Worker-0.1.0-arm64-dev.dmg
```

Runtime preparation uses the checked-in `python/uv.lock` and a Python 3.12.11
build-time interpreter. It does not depend on Homebrew Python at runtime.

Live tests are opt-in and may use the target Mac, the local reference voice, or
the beta service. They are disabled unless their explicit environment variable
is set; ordinary tests do not need network, model files, or production
Keychain items.

## Local cleanup

Uninstalling the app does not remove configuration, Keychain entries, models,
reference audio, logs, or unacknowledged spool files. Remove those only through
an explicit owner action after checking their contents.
