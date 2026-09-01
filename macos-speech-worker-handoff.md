# Doublangu macOS Speech Worker — Mac-Verified Handoff

Status: reviewed on 2026-09-01; implementation has not started. Checkpoint 1 must
prove the local voice/model/reference combination, and checkpoint 2 must correct
the backend profiles and cancellation behavior before a worker enrolls.

Repository baseline: `85dbb0721ee2f7fb7cc558f9222be0b6ab7373aa`

This is a self-contained handoff plan, not an AFlow plan. It covers the smallest
usable path from one owner-operated Apple-Silicon Mac to the existing Doublangu
audible-reader backend.

## 1. Objective And Observable Success

Build a native macOS menu-bar worker that:

1. leases Dutch speech work from `https://nlrn.evren.io/beta` over outbound HTTPS;
2. renders words and short phrases with the installed Dutch Apple voice;
3. renders sentences with Chatterbox Multilingual v3 through MLX-Audio;
4. uploads mono 24 kHz AAC-LC M4A files to Doublangu and deletes a local result
   only after the server accepts or explicitly rejects that lease attempt;
5. survives app, network, login, and Mac restarts without losing an accepted
   lease or exposing credentials; and
6. releases Chatterbox model memory after 10 minutes without Chatterbox work,
   then cold-loads it on the next Chatterbox lease while heartbeating the lease.

Success is visible when a beta article plays real Dutch word pronunciation and
ordered sentence narration from authenticated server URLs, the local acknowledged
spool is empty, the Chatterbox child is absent after its idle timeout, and a later
sentence request restarts the child and completes without owner intervention.

## 2. Now (MVP) And Later

### 2.1 Now (MVP)

1. One owner-operated M4 Mac and one enrolled worker.
2. One total speech job at a time. Do not run AVSpeech and Chatterbox jobs in
   parallel by default on this 32 GB machine.
3. Native SwiftUI `MenuBarExtra` app with setup, status, start/stop, launch-at-login,
   diagnostics, and explicit re-enrollment actions.
4. In-process Swift lease loop, journal, AVSpeech renderer, postprocessor, and
   uploader.
5. One on-demand bundled Python/MLX-Audio child for Chatterbox. Model files remain
   outside the app bundle.
6. User-scoped installation and opt-in `SMAppService.mainApp` launch at login.
7. Real beta integration and restart/offline/idle-unload acceptance on this Mac.

### 2.2 Later / Out Of Scope

1. Multiple Macs or profile-aware server routing across heterogeneous workers.
2. More languages, alternate voices/models, or a profile-management UI.
3. App Store distribution, automatic updates, or background system daemons.
4. More than one concurrent speech job.
5. Browser-to-Mac audio, inbound public/Tailscale service ports, or local audio
   serving.
6. URL/ebook ingestion, forced alignment, mobile/offline clients, or audio lessons.
7. Changes to the separate AudioVentura ACE Node. This plan only borrows proven
   source patterns from it.

## 3. Verified Target-Mac Reality

The implementation and acceptance target is the Mac that produced this snapshot:

1. MacBook Air `Mac16,12`, Apple M4, 10 CPU cores, 32 GB unified memory.
2. arm64 macOS 15.7.9 build `24G830`, SIP enabled.
3. Xcode 26.3 build `17C529`, Swift 6.2.4, and `swift-format` 6.2.3.
4. Homebrew `uv` 0.8.2 and system Python 3.14.6. The installed app must not depend
   on either one at runtime.
5. About 26 GiB filesystem space was available during review.
6. Installed Dutch voices:
   - `com.apple.voice.compact.nl-NL.Xander` (`nl-NL`, current MVP choice);
   - `com.apple.voice.compact.nl-BE.Ellen` (`nl-BE`, not used by this MVP).
7. AudioVentura ACE Node is installed and running from `/Applications`; its app
   bundle is about 1.3 GB and its private model state is about 24 GB.
8. The AudioVentura runtime proves Python 3.12.11, MLX 0.32.2, and PyTorch MPS on
   this Mac, but it does not contain `mlx_audio` and must not be modified or shared
   in place by Doublangu.
9. Swap was heavily used during review. Treat that as a coexistence warning, not
   proof that a cold Chatterbox model fits beside a busy ACE workload.

At implementation start, record the same fields again. A changed OS build, voice
identifier, free-space result, or hardware target invalidates the corresponding
profile proof and must be resolved before beta work is queued.

## 4. Actual Backend Contract And Required Corrections

The backend already implements the worker boundary in:

- `contracts/speech-worker-v1.schema.json`;
- `contracts/openapi.yaml`;
- `internal/httpapi/speech_workers.go`;
- `internal/workers/service.go`;
- `internal/jobs/jobs.go`;
- `internal/speech/store.go` and `internal/speech/types.go`;
- `internal/store/migrations/005_audible_reader.sql`.

The following behavior is already implemented and should be used as-is:

1. one-time enrollment and a separate worker credential;
2. 25-second long polling and `204 No Content` when no compatible work appears;
3. 90-second leases renewed by 30-second heartbeats;
4. strict JSON/multipart validation and mono 24 kHz AAC-LC M4A limits;
5. idempotent exact duplicate completion for the same attempt and lease token;
6. atomic blob/render/job publication and nondeterministic-digest rejection;
7. owner revocation and bounded three-attempt job retry; and
8. server-owned media URLs, retention, range serving, and narration composition.

### 4.1 Required backend correction: real speech profiles

`internal/speech/store.go:DefaultProfilesTx` currently creates placeholder profiles:

1. Apple `Samantha` while the article/job language is Dutch;
2. Chatterbox model `worker-v3`, voice `default`, and an empty reference hash.

Those jobs are not renderable by the target configuration. After checkpoint 1
selects the real MLX-Audio/model commit and reference clip, replace the placeholders
with immutable target values:

1. AVSpeech:
   - engine `avspeech`;
   - model revision containing macOS build `24G830`;
   - language `nl`;
   - voice `com.apple.voice.compact.nl-NL.Xander`;
   - empty reference hash;
   - speed 1000, pitch 0;
   - a new explicit AVSpeech rate/postprocess mapping version.
2. Chatterbox:
   - engine `chatterbox`;
   - exact Hugging Face model revision from checkpoint 1;
   - language `nl`;
   - stable voice label `doublangu-nl-reference-v1`;
   - exact lowercase SHA-256 of the accepted Dutch reference WAV;
   - speed 1000, pitch 0;
   - a new explicit MLX-Audio/Chatterbox mapping version.

Keep these values source-controlled for the single-Mac MVP. Do not add a profile
admin API, database editor, or generalized profile registry. A future profile
change creates a new immutable profile and request identity.

### 4.2 Required backend correction: observable cancellation

Active cancellation currently clears `lease_owner`, `lease_token_hash`, and lease
expiry before the worker can receive the contract's `cancel_requested: true`.
Correct it in `internal/jobs/jobs.go` and replace the duplicated direct cancellation
SQL in `internal/speech/store.go` and `internal/reader/v2_store.go`:

1. queued cancellation clears empty lease fields as it does today;
2. leased/running cancellation changes the state/error but retains the hashed token,
   owner, and existing expiry long enough for the matching attempt to heartbeat;
3. `VerifyLease` permits that exact canceled attempt/owner/token only for status
   acknowledgement, never for completion;
4. heartbeat returns `200` with `cancel_requested: true` and does not extend the
   lease; and
5. completion under a canceled lease remains `409` and cannot publish bytes.

Add tests proving cancellation during Chatterbox generation is seen on the next
heartbeat and that another worker/token cannot inspect or mutate it.

### 4.3 Required backend correction: one preferred lexical profile

`QueueArticleAudioTx` currently inserts another `preferred=1` lexical binding when
a new profile creates a new render. Before inserting/upserting the new binding:

1. set prior pronunciation bindings for that occurrence to `preferred=0`;
2. insert or update the selected render as the only `preferred=1` row; and
3. test that the reader returns the new render rather than the lowest old render ID.

Before beta deployment, query for existing `Samantha` or `worker-v3` profiles/jobs.
The 2026-08-31 handoff did not claim a deployed speech worker, so do not add data
migration machinery speculatively. If such rows exist in the actual beta database,
stop checkpoint 2 and add a narrow transactional reconciliation for only those
placeholder renders before enabling the worker.

### 4.4 Deliberately unchanged backend behavior

1. A capability continues to mean engine, language, unit kind, and output limits.
   The worker must additionally compare every leased profile field with its local
   immutable configuration and fail closed. Profile-specific multi-worker routing
   is later work.
2. Successful completion continues to return the implemented `{"ok": true}`.
   Because the server validates the request hash/digest and exact duplicate
   completion is idempotent, that `200` is the durable acknowledgement.
3. The worker routes continue below the deployment prefix `/beta`; no new route or
   inbound Mac endpoint is required.

## 5. Product And Process Architecture

### 5.1 Native app instead of a LaunchAgent daemon

Use an arm64 SwiftUI menu-bar app with minimum macOS 14 and Swift 6 language mode.
The app owns the lease loop and starts at login only when the owner enables
`SMAppService.mainApp`.

Do not build the old handoff's separate daemon/CLI/LaunchAgent architecture. The
menu app provides the owner-visible controls and uses a foreground development
launch plus tests for diagnosis.

### 5.2 Borrow from the proven AudioVentura app

Adapt, with Doublangu names and tests, the useful seams under
`/Users/evren/code/audioventura/deploy/node/macos/app`:

1. `AppPaths.swift` private Application Support/Cache/Logs layout and permissions;
2. `KeychainStore.swift` generic-password implementation and injectable secret
   protocol;
3. `LogRotation.swift` bounded private logs;
4. `WorkerSupervisor.swift` process abstraction, PID/executable/start-identity/app-
   revision receipt, bounded crash backoff, sleep activity, and login-item seams;
5. `ReleaseManifest.swift` strict embedded provenance;
6. source-controlled runtime/app/DMG/release verification scripts and their tests.

Do not copy AudioVentura's Tailscale discovery, node/supervisor bearer tokens,
health/drain API, 25 GB model manifest, database, or ACE-specific environment.
Doublangu uses outbound HTTPS and a loopback-only disposable Chatterbox child.

### 5.3 Process boundaries

The Swift app owns:

1. setup/configuration and Keychain access;
2. enrollment, long polling, heartbeat, failure, and completion requests;
3. the one-job scheduler, crash-safe journal, and upload spool;
4. AVSpeech rendering and common audio postprocessing;
5. Chatterbox child identity/lifecycle and loopback client;
6. menu status, logs, sleep activity, and launch-at-login.

The Python child owns only MLX-Audio model loading and Chatterbox generation. It
binds `127.0.0.1` on one app-selected port, receives only the sentence, model
revision, `lang_code=nl`, and fixed reference-audio path, and writes/returns only
bounded local audio. It receives no Doublangu credentials.

## 6. Repository Layout

Create this tree in the Doublangu repository:

```text
macos/
  AGENTS.md
  speech-worker/
    README.md
    VERSION
    app/
      Package.swift
      Sources/DoublanguSpeechWorker/
        DoublanguSpeechWorkerApp.swift
        AppState.swift
        AppPaths.swift
        Configuration.swift
        KeychainStore.swift
        LogRotation.swift
        ReleaseManifest.swift
        WorkerProcess.swift
        ChatterboxSupervisor.swift
        WorkerClient.swift
        ProtocolModels.swift
        LeaseLoop.swift
        JobJournal.swift
        AVSpeechRenderer.swift
        ChatterboxRenderer.swift
        AudioPostprocessor.swift
        MenuContentView.swift
        SetupView.swift
        SettingsView.swift
      Tests/DoublanguSpeechWorkerTests/
    python/
      pyproject.toml
      uv.lock
    Resources/
      Info.plist.in
      DoublanguSpeechWorker.entitlements
    build-runtime.sh
    build-app.sh
    build-dmg.sh
    notarize-dmg.sh
    verify-release.sh
```

Keep the schema authoritative at repository root. Tests may copy schema fixtures at
build time, but there must not be a separately edited protocol definition.

## 7. Dependencies, Model, And Storage Gate

### 7.1 Swift and Python

1. Use only Apple frameworks in Swift: SwiftUI, Foundation, Security, AVFAudio,
   AVFoundation, CryptoKit, ServiceManagement, and OSLog as needed.
2. Bundle a relocatable arm64 Python 3.12.11 runtime using the proven AudioVentura
   builder pattern. Do not use system Python 3.14.6 or Homebrew at app runtime.
3. Start model evaluation with `mlx-audio` 0.4.7 because the current v3 model card
   names that converter version, but pin the exact package release/commit only after
   the real Dutch smoke passes.
4. Use `mlx-community/chatterbox-multilingual-v3`, currently about 2.71 GB, plus its
   required `mlx-community/S3TokenizerV2`. Record and pin both resolved revisions.
5. The installed app bundle contains the Python runtime, lock/receipt, and source
   provenance, but no model, reference voice, secret, cache, or generated audio.

Primary upstream references:

- <https://github.com/Blaizzy/mlx-audio/blob/main/docs/models/tts/chatterbox.md>
- <https://github.com/Blaizzy/mlx-audio/blob/main/docs/guides/web-ui-api-server.md>
- <https://huggingface.co/mlx-community/chatterbox-multilingual-v3>
- <https://developer.apple.com/documentation/avfaudio/avspeechsynthesizer>

### 7.2 Storage gate

1. Require at least 12 GiB available before runtime/model preparation and recommend
   20 GiB. The reviewed Mac had about 26 GiB free.
2. Before locking that threshold, checkpoint 1 records the actual runtime archive,
   expanded runtime, model/tokenizer snapshot, download staging, and one render's
   temporary peak. Raise the threshold if their sum plus 4 GiB safety exceeds
   12 GiB; never lower it below 12 GiB.
3. Model directories and download caches use private permissions and are excluded
   from backup. A completed, verified revision is activated atomically.
4. Cleanup is explicit and may remove only an older verified model revision or an
   incomplete download. Never touch AudioVentura storage.

## 8. Configuration, Secrets, And Local State

Use these private paths:

```text
~/Library/Application Support/Doublangu Speech Worker/config.json
~/Library/Application Support/Doublangu Speech Worker/Reference/dutch-reference-v1.wav
~/Library/Application Support/Doublangu Speech Worker/Models/<revision>/
~/Library/Application Support/Doublangu Speech Worker/Spool/
~/Library/Application Support/Doublangu Speech Worker/State/{setup,worker}.json
~/Library/Caches/Doublangu Speech Worker/model-download/
~/Library/Logs/Doublangu Speech Worker/worker.log
```

Nonsecret configuration contains:

1. base URL `https://nlrn.evren.io/beta` and protocol `speech-worker.v1`;
2. worker ID/name after enrollment;
3. exact Apple voice and both profile identities;
4. model/tokenizer revisions and reference path/hash;
5. output/postprocess version and port range;
6. one-job capacity, 512 MiB spool cap, and Chatterbox idle timeout `600` seconds.

Store three independent generic-password values in Keychain service
`io.evren.doublangu.speech-worker`:

1. beta perimeter Basic username;
2. beta perimeter Basic password;
3. Doublangu worker token.

Accept the one-time enrollment token only in a `SecureField`, exchange it once, and
do not persist it. Never place any secret in config, plist, process arguments,
environment, logs, crash text, model paths, or release receipts.

The spool is not a cache. It retains only accepted leases, journals, partial files,
and completed artifacts awaiting an unambiguous server result. At 512 MiB, stop
leasing new work and keep retrying existing uploads. Never evict an unacknowledged
result to make room.

## 9. Protocol Client And Journal Rules

Use the exact implemented routes:

1. `POST /api/v1/speech-worker/enroll` with
   `X-Doublangu-Enrollment-Token`;
2. `POST /api/v1/speech-worker/lease` with
   `X-Doublangu-Worker-Token`;
3. `POST /api/v1/speech-worker/jobs/{id}/heartbeat` with worker and lease token
   headers;
4. multipart `POST /api/v1/speech-worker/jobs/{id}/complete`, metadata field
   `metadata`, file field `audio`, and the lease token inside completion metadata;
5. `POST /api/v1/speech-worker/jobs/{id}/fail` with worker and lease token headers.

Every request also carries perimeter Basic authentication. Build URLs without
discarding `/beta`. Use the system trust store and never disable TLS verification.

For one lease attempt:

1. validate protocol, IDs, dates, hashes, job type, language/unit, limits, and every
   profile field before writing the journal;
2. atomically persist job/attempt/lease token/request hash/profile and planned paths;
3. start a 30-second heartbeat task before any cold model load or render;
4. write engine output to `*.partial`, fsync/close, validate, postprocess, checksum,
   atomically rename to `*.ready`, and update the journal;
5. upload exactly the ready bytes and metadata;
6. on `200 {"ok": true}`, delete the artifact and journal;
7. on timeout/network/5xx, retain the exact artifact and retry completion without
   synthesizing again;
8. on explicit `cancel_requested`, stop promptly and delete the canceled attempt's
   local partial/ready data after recording the terminal reason;
9. on `409`, treat the attempt as explicitly stale/rejected, never publish or
   re-render it locally, remove its artifact, and wait for a new lease; and
10. on authentication/protocol/profile mismatch, stop leasing the affected
    capability and show an actionable owner-visible error.

On app startup, scan journals before leasing. Resume an exact ready upload; discard
an interrupted render only after determining the lease is canceled/stale or after
the server leases a later attempt. Never create a second artifact for the same live
attempt while a ready artifact exists.

## 10. Rendering And Memory Lifecycle

### 10.1 AVSpeech word/phrase renderer

1. Resolve only `com.apple.voice.compact.nl-NL.Xander`; do not switch silently.
2. Require engine `avspeech`, language `nl`, word/phrase unit, and exact profile.
3. Create one `AVSpeechUtterance` with exact server text and deterministic speed/
   pitch mapping.
4. use `AVSpeechSynthesizer.write(_:toBufferCallback:)`, retain the synthesizer,
   detect its terminal empty buffer, and honor cancellation/deadline;
5. write linear PCM to a partial file and pass it through the common postprocessor;
6. reject empty, nonfinite, clipped, or excessive-duration output.

The AVSpeech renderer is serial and never uses Chatterbox as a fallback.

### 10.2 Chatterbox sentence renderer

1. Require engine `chatterbox`, language `nl`, sentence unit, exact model/reference/
   mapping profile, and a verified local model receipt.
2. If the child is absent, start the exact bundled Python executable with a minimal
   explicit environment and loopback bind, then poll a bounded readiness probe.
3. Heartbeat throughout child start, model cold load, generation, postprocessing,
   and upload.
4. Send exact sentence text, `lang_code=nl`, the pinned model, and the one fixed
   reference WAV. Do not accept server-provided paths or model names.
5. bound response bytes/time, validate the returned WAV/PCM, and postprocess it.
6. After the Chatterbox attempt is terminal and no Chatterbox journal/upload remains,
   start a 600-second idle timer.
7. If no new Chatterbox job arrives before the timer fires, terminate only the child
   matching the recorded PID, executable, start identity, and app revision. Wait up
   to 10 seconds, then force-kill only if the same identity still matches.
8. Confirm the child is absent and record `model_unloaded_idle`; keep model files and
   the Swift worker resident.
9. A later Chatterbox lease cancels the idle timer, starts a fresh child, cold-loads
   the model, and continues under heartbeat. A start/load failure is retryable once
   through the server attempt policy, then suppresses Chatterbox capability during
   bounded local backoff.

Process exit is the required MLX/MPS memory-release boundary. Do not depend on a
Python object deletion or undocumented MLX cache call as proof of offload.

### 10.3 Common postprocessing

Use AVFoundation in-process, not the Mac's Homebrew `ffmpeg`:

1. mono mixdown and 24,000 Hz resample;
2. fixed leading/trailing digital-silence trim with small lexical/sentence padding;
3. peak limit at -1 dBFS without amplifying pathological noise;
4. AAC-LC M4A at a 48 kbit/s target;
5. exact duration, byte size, codec/container/rate/channels, and SHA-256 report.

The postprocess version is part of the backend profile/request identity. Byte-level
AAC differences across OS builds require a new profile, never overwrite of an
accepted request hash.

## 11. Menu, Reliability, And Release Behavior

The menu app shows only bounded operational state:

1. Setup required / enrollment required;
2. Ready (model cold) / loading model / rendering / uploading;
3. idle countdown and model unloaded;
4. offline/backing off / spool full / profile mismatch / failed;
5. last server contact and current job type, never text or credentials.

Actions:

1. Setup runtime/model/reference;
2. Enroll or replace enrollment;
3. Start/stop worker;
4. Restart Chatterbox child;
5. Run diagnostics and reveal the private log in Finder;
6. Enable/disable Launch at Login;
7. remove an explicitly selected old model revision.

Use 10 MiB logs with five retained files and private permissions. Acquire a
`ProcessInfo.beginActivity` idle-sleep assertion only while a lease, render, model
load, upload, or spool recovery is active; release it while long-polling/idle.

Development builds are arm64 and ad-hoc signed. A distributable DMG requires the
owner's Developer ID, inside-out hardened-runtime signing, notarization, stapling,
Gatekeeper verification, and explicit publication approval. Uninstall removes only
the app by default; config, Keychain, reference, model, logs, and unacknowledged
spool require separate explicit cleanup choices.

## 12. Sequential Implementation Checkpoints

### 12.1 Checkpoint 1 — Real Mac voice/model/reference proof

1. Record `sw_vers`, `uname -m`, hardware/memory, Xcode/Swift/uv versions, free disk,
   memory pressure, and the two installed Dutch voice identifiers.
2. Obtain one clean, licensed native-Dutch reference WAV from the owner and place it
   at the planned Reference path; record its SHA-256, sample rate, channels, length,
   and provenance. This owner input is required—do not synthesize or download an
   arbitrary speaker.
3. Build a temporary locked Python 3.12.11 environment, install the starting
   MLX-Audio candidate, and pin the resolved MLX-Audio, Chatterbox, and S3Tokenizer
   revisions only after success.
4. Render one Dutch word through Xander and one Dutch sentence through Chatterbox.
5. Measure cold load, warm generation, peak disk, process RSS, memory pressure/swap,
   and output metadata. Run the same smoke with the installed AudioVentura menu app
   resident; do not submit or disturb an AudioVentura job.
6. Terminate the Chatterbox test process, prove it is absent, and record the observed
   memory-pressure change. Wait 10 minutes and repeat one cold request to validate
   the intended idle lifecycle.
7. Write the exact profile values needed by checkpoint 2 into the implementation
   record. Do not alter backend code in this checkpoint.

Done when both Dutch outputs are listenable and structurally valid, a cold reload
fits the 90-second renewable lease design, and the owner-provided reference/model
identities are exact. If coexistence is unsafe, keep total capacity one and require
the owner to stop the ACE Node during Chatterbox use; do not invent memory swapping.

### 12.2 Checkpoint 2 — Backend profile and cancellation correction

1. In the authoritative p100 checkout, implement sections 4.1–4.3 in
   `internal/speech/store.go`, `internal/jobs/jobs.go`, and
   `internal/reader/v2_store.go`.
2. Update focused tests in `internal/speech/store_test.go`,
   `internal/jobs/jobs_test.go`, `internal/workers/service_test.go`, and reader/HTTP
   tests affected by preferred audio selection or cancellation.
3. Keep `contracts/speech-worker-v1.schema.json` and the route shapes unchanged
   unless a test proves an actual mismatch. Regenerate OpenAPI TypeScript and require
   no unrelated generated change.
4. Correct `ARCHITECTURE.md` and `DEVLOG.md` references from the nonexistent
   `plans/macos-speech-worker-handoff.md` to this root handoff path.
5. Check the beta database for placeholder profile rows as specified in section 4.3.
6. After the owner explicitly authorizes commit/push and beta deployment, publish
   the backend correction, deploy the exact revision to beta, and exercise
   enrollment/lease/heartbeat/cancel/duplicate-complete with a fake renderer before
   enabling the Mac app.

Done when a beta lease contains Xander or the exact Chatterbox profile, active
cancellation produces `cancel_requested: true`, duplicate completion remains
idempotent, and one occurrence exposes only its current preferred lexical render.

### 12.3 Checkpoint 3 — Native shell, runtime, setup, and provenance

1. Create the tree in section 6 and the nearest `macos/AGENTS.md`.
2. Port the approved AudioVentura seams listed in section 5.2 with Doublangu bundle
   ID `io.evren.doublangu.speech-worker`, paths, labels, and tests.
3. Implement the bundled Python 3.12.11 runtime builder, exact lock/receipt, model
   preparation, storage gate, reference validation, release manifest, and menu setup.
4. Implement Keychain, private paths, log rotation, login item, sleep activity, and
   process receipt tests with injected fakes.
5. Build an ad-hoc source-only development app with no model in its bundle.

Done when setup fails closed on wrong platform/storage/receipt/reference, the app
builds arm64, no secret/model enters the bundle, and borrowed supervisor seams pass
their renamed tests.

### 12.4 Checkpoint 4 — Protocol, journal, and AVSpeech beta slice

1. Implement strict models/client, enrollment, Basic plus worker auth, path-prefix
   handling, one-job lease loop, heartbeat, journal/spool, and fake server tests.
2. Implement Xander buffer rendering and common postprocessing.
3. Prove timeout/restart/idempotent upload without a second synthesis.
4. Enroll the development app against beta and complete one real word/phrase job.
5. Play the resulting authenticated server URL in the beta reader, stop the app,
   play it again, and confirm the acknowledged local spool is empty.

Done when real Dutch lexical audio survives with the worker stopped and no request
or secret appears in logs.

### 12.5 Checkpoint 5 — Chatterbox cold/warm/idle lifecycle beta slice

1. Implement on-demand child start, bounded readiness, exact profile/reference gate,
   sentence request, heartbeat during cold load, and crash backoff.
2. Implement the 600-second idle timer and exact-identity child termination.
3. Add fake-child tests for cold start, warm reuse, idle termination, timer cancel,
   crash, hung shutdown, wrong PID identity, cancellation, and capability suppression.
4. Complete one real beta sentence from cold and a second while warm; record latency,
   memory, and output metadata.
5. Wait 10 minutes, prove the child is gone, request another sentence, and prove a
   fresh child cold-loads and completes.
6. Repeat the idle and one cold-load test with the AudioVentura app resident.

Done when the reader plays the ordered server-stored narration, the model process
reliably disappears after idle, and the next request works without reconfiguration.

### 12.6 Checkpoint 6 — Durability, release, and owner handoff

1. Verify recovery across network loss during render/upload, Swift app kill,
   Chatterbox kill, logout/login, and Mac reboot.
2. Verify cancellation during cold load and generation, full spool behavior, worker
   revocation, and profile mismatch.
3. Build and verify the arm64 app/DMG. Notarize or publish only with explicit owner
   approval.
4. Update `macos/speech-worker/README.md`, root `ARCHITECTURE.md`, and `DEVLOG.md`
   with only behavior proven on beta.
5. After explicit owner authorization, commit every completed implementation
   checkpoint and finish with a pushed PR or merged `main` record; never leave
   authorized implementation only in a worktree.

Done when login/reboot recovery and idle unloading are live-proven on this Mac, beta
is manually accepted by the owner, and the exact committed revision is discoverable
on GitHub.

## 13. Verification Commands And Expected Results

### 13.1 Backend checkpoint

Run in the authoritative p100 checkout:

```sh
go test ./internal/jobs ./internal/speech ./internal/workers ./internal/reader ./internal/httpapi ./cmd/doublangu-server
go test -race ./internal/jobs ./internal/speech ./internal/workers ./internal/reader ./internal/httpapi
npm --prefix web run validate:openapi
npm --prefix web run generate:api
git diff --exit-code -- web/src/lib/api/generated.ts
make verify
git diff --check
```

Expected: all focused and broad checks pass; generated API is stable unless the
contract intentionally changed; cancellation and preferred-profile tests fail on
the old code and pass on the correction.

### 13.2 macOS app checkpoint

Run from `macos/speech-worker`:

```sh
xcrun swift-format lint --recursive app/Sources app/Tests
swift test --package-path app --parallel
swift build --package-path app -c release
uv lock --project python --check
uv sync --project python --frozen
./build-app.sh --development
./verify-release.sh --app-only ../../dist/macos/Doublangu\ Speech\ Worker.app
git diff --check
```

Add opt-in live tests with explicit names, disabled by default:

```sh
DOUBLANGU_TEST_AVSPEECH_LIVE=1 swift test --package-path app --filter AVSpeechLive
DOUBLANGU_TEST_CHATTERBOX_LIVE=1 swift test --package-path app --filter ChatterboxLive
DOUBLANGU_TEST_SERVER_LIVE=1 swift test --package-path app --filter ServerIntegrationLive
DOUBLANGU_TEST_IDLE_UNLOAD_LIVE=1 swift test --package-path app --filter IdleUnloadLive
```

Expected: ordinary tests require no model, network, Keychain production item, or
running app; each live test records exact platform/profile/timing/memory evidence and
cleans only its own test state.

## 14. Manual Acceptance Checklist

1. Enroll through a hidden one-time token and confirm no secret is in files/logs.
2. Enable Launch at Login and confirm the app returns after login without prompts.
3. Create or reuse a beta Dutch article and verify Xander word/phrase playback.
4. Start narration from a cold Chatterbox state and verify ordered Dutch sentences
   in the fixed reference voice.
5. Disconnect/reconnect network during upload and prove exactly one accepted blob.
6. Clear narration during generation and observe `cancel_requested` before local
   cleanup; lexical audio remains.
7. Kill the Python child and verify bounded recovery without duplicate output.
8. Let the app idle for 10 minutes, confirm the child PID is absent, then request a
   new sentence and confirm cold reload plus completion.
9. Stop the Swift app and replay all acknowledged audio from server URLs.
10. Confirm acknowledged spool entries are gone, model/reference/config remain, and
    the app exposes no inbound non-loopback listener.
11. Repeat the cold-load/idle-unload check with AudioVentura resident and record
    whether the two apps can coexist under acceptable memory pressure.

## 15. Stop And Escalate Conditions

Stop instead of expanding the design if:

1. the owner has not supplied a licensed clean Dutch reference clip;
2. Chatterbox v3 cannot cold-load and heartbeat reliably on this M4/32 GB Mac;
3. process termination does not release the MLX/MPS memory sufficiently;
4. coexistence with AudioVentura causes sustained critical memory pressure and the
   owner does not accept running only one model worker at a time;
5. Xander cannot render through the buffer API on the current macOS build;
6. beta still queues placeholder profiles or cancellation is not observable;
7. reliable operation would require disabled TLS, plaintext secrets, an inbound
   public Mac port, browser access to localhost, or deletion before server outcome;
8. storage preparation cannot retain the required 4 GiB safety margin; or
9. implementation begins adding multi-worker routing, a generic workflow engine,
   an updater, or another product feature from the Later list.

## 16. Final Handoff Evidence

The implementing agent's final report must include:

1. exact Mac model, macOS build, Xcode/Swift, Python, MLX-Audio, model/tokenizer
   revisions, Xander identifier, reference hash, and output profile;
2. exact backend and app commits plus beta deployed revision;
3. automated, live, and manual results separated clearly;
4. cold/warm generation latency, peak storage/memory evidence, idle timeout, child
   PID disappearance, and cold reload result;
5. one real lexical and one real sentence server URL/digest proof without exposing
   credentials;
6. spool deletion only after `200` or explicit rejection/cancellation;
7. launch-at-login, network, app/child kill, and reboot recovery evidence; and
8. known pronunciation/model quality limitations separated from product blockers.
