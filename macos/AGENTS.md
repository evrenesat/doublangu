# macOS speech worker

This directory contains the user-scoped native Doublangu speech worker. It is
developed and tested on the target Apple-Silicon Mac; it is not part of the Go
server build and must not be moved to the p100 server checkout.

The Swift package uses only Apple frameworks. The Python child is a disposable,
loopback-only MLX-Audio process. Models, reference audio, credentials, logs,
and the upload spool stay outside the app bundle.

From `macos/speech-worker`, use:

```sh
xcrun swift-format lint --recursive app/Sources app/Tests
swift test --package-path app --parallel
swift build --package-path app -c release
```

Do not commit model snapshots, runtime virtual environments, reference audio,
Keychain values, generated audio, or local setup state.
