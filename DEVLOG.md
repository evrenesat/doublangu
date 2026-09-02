# Development Log

## 2026-09-02 — Luna relational correction repair

- Traced the live beta article failure to three independent weak-model relation
  mistakes: sentence ordinal values used as substring occurrences, undefined or
  duplicated local sense references, and construction tokens outside their
  declared spans. Strict local validation rejected every malformed response.
- Made the chunk prompt explicit about cross-field sense definitions, exact
  occurrence semantics, unique definitions, and construction span membership.
  The one correction now receives the fail-fast error plus independently
  collected relation diagnostics, and a second bounded correction is available
  when the first repair exposes or introduces another error.
- Closed a validator/store contract gap exposed after all three live paragraphs
  passed: deterministic `normalized_form` equality is now checked before cache
  or persistence rather than first failing inside the publication transaction.
- Bumped the prompt identity to `reader-analysis-prompt.v4` so older caches and
  jobs cannot be reused, while retaining the existing strict schema and
  validator as the publication gate.
- Added regression coverage for simultaneous reference, occurrence, token, and
  construction errors, bounded second-correction behavior, and an opt-in live
  paragraph smoke selectable by runtime model and effort.

### Verification

- Formatting and `go mod tidy -diff` — passed.
- `go test ./... -count=1 -buildvcs=false` — passed.
- Race tests for semantics, annotator, analysis, reader, speech, and HTTP API —
  passed.
- OpenAPI validation/regeneration — passed with no generated diff.
- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 70 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 8 tests passed in the
  isolated rerun; one concurrent run first timed out during navigation while
  other web checks were active.
- `make verify` — passed.
- Authenticated legacy and `gpt-5.6-luna / medium` chunk live tests — passed;
  the live chunk satisfied the unchanged deterministic validator.
- `git diff --check` — passed.

## 2026-09-02 — Analysis reliability recovery follow-up

- Changed owner settings writes to reject a changed model/effort pair while
  the runtime model catalog is stale after a failed refresh. Re-sending the
  already persisted pair remains idempotent, while the Svelte Save action is
  disabled and explains that a successful refresh is required for changes.
- Extended startup recovery to finalize orphaned `analysis_run` rows as
  failed with `v1.analysis_interrupted`, completion time, and elapsed duration.
  Completed paragraph counts, failed indexes, provenance, stderr, and turn
  artifacts remain available for retry diagnostics; the article stays queued.
- Added backend, UI, and SQLite regression coverage for both failure modes.

### Verification

- Focused normal and race Go tests for `internal/httpapi`, `internal/reader`,
  and `internal/analysis` — passed.
- `/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin/go test ./... -count=1 -buildvcs=false` — passed.
- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit` — 70 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 8 tests passed.
- `npm --prefix web run validate:openapi` — passed.
- `npm --prefix web run generate:api` followed by a generated-file diff check —
  passed with no generated changes.
- `make verify` — passed with the checkout's temporary Git
  `safe.directory=/root/code/doublangu` environment override.
- `DOUBLANGU_TEST_CODEX_LIVE=1 go test ./internal/annotator -run Live -count=1 -v -buildvcs=false` — passed against the authenticated Codex CLI.
- `git diff --check` — passed.
- Deployed isolated beta release
  `6bcddfd4925254cacbb07b1cc5edace5889ff423f5079c56ec7b632653d01b7f`.
  The beta service, database readiness, migration 006 analysis tables, Codex
  service-account login, TLS, and unauthenticated Basic Auth challenge passed;
  protected credentials and production remained unchanged.

## 2026-09-01 — Weaker-model analysis reliability and lexical speech repair

- Added paragraph-isolated Codex app-server analysis with compact validated
  sense carry-forward, dynamic exact-anchor schemas, one corrective turn, and
  atomic whole-article publication after the existing full validator passes.
- Added exact prepared-input/model/effort cache identities, durable run and turn
  diagnostics, persisted model/effort selection, owner settings/history/detail
  APIs and UI, and fail-closed behavior when no model is selected.
- Corrected lexical AVSpeech jobs to use visible Dutch occurrence text while
  retaining IPA as pronunciation metadata; regeneration supersedes the old
  preferred render without deleting history.
- Documented cache identity, settings precedence, diagnostics growth/privacy,
  manual comparison, paragraph isolation, and the lexical speech boundary in
  `README.md` and `ARCHITECTURE.md`.

### Verification

All Go commands below used
`/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin` on
`PATH`; web commands used `/tmp/doublangu-node-v24.20.0/bin` on `PATH`.

- `test -z "$(gofmt -l internal/semantics internal/annotator internal/analysis internal/reader internal/speech internal/httpapi internal/config cmd/doublangu-server)` — passed.
- `go mod tidy -diff` — passed.
- Focused normal Go tests for semantics, annotator, analysis, reader, speech,
  HTTP API, store, config, and server — passed.
- Focused Go race tests for semantics, annotator, analysis, reader, speech,
  and HTTP API — passed.
- `npm --prefix web run validate:openapi` — passed.
- `npm --prefix web run generate:api` — passed; a second generation was
  reproducible and the only generated diff was the intended API contract
  addition, which is included in the implementation commit.
- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 70 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 8 tests passed.
- `make verify` — passed with the checkout's temporary Git
  `safe.directory=/root/code/doublangu` environment override.
- `git diff --check` — passed.
- `DOUBLANGU_TEST_CODEX_LIVE=1 go test ./internal/annotator -run Live -count=1 -v` — passed during the 2026-09-02 follow-up review.

## 2026-09-01 — macOS speech lease timestamp compatibility

- Updated the native speech worker to accept the server's millisecond-precision
  lease expiry timestamps while retaining whole-second compatibility. Valid
  leases no longer stop the worker with the misleading `Profile mismatch`
  status.
- Rebuilt and reinstalled the configured local app without moving its private
  model, reference audio, configuration, or Keychain state.
- Live beta verification drained all 11 queued speech jobs. Every job completed
  successfully and all 11 audio renders became ready; the two-sentence proof
  article moved from narration `queued` to `ready`.

### Verification

- `xcrun swift-format lint --recursive app/Sources app/Tests` — passed.
- `swift test --package-path app --parallel` — 24 tests passed.
- `swift build --package-path app -c release` — passed.
- `./build-app.sh --development` — passed.
- `./verify-release.sh --app-only ../../dist/macos/Doublangu\ Speech\ Worker.app` — passed.
- Installed-app deep code-signature verification and binary parity with the
  verified build — passed.
- `git diff --check` — passed.

## 2026-09-01 — Audio base-path and terminal speech recovery fixes

- Added base-aware audio URL normalization at the SvelteKit web boundary. Article
  and narration API responses now give browser audio controls `/beta/api/...`
  URLs when the app is built under `/beta`, preserve absolute URLs, and avoid
  double-prefixing an already-prefixed path. Legacy article responses without
  v2 arrays remain compatible.
- Added a transactional terminal-job recovery hook. When the third leased speech
  attempt expires, recovery marks queued/generating renders failed and recomputes
  every bound article's narration status and error code in the same SQLite
  transaction. Startup, server analysis, and speech-worker recovery all use it.
- Added `/beta` path coverage and a three-expiry generating-render regression.

### Verification

All Go commands below used `/tmp/doublangu-go1.26.5/bin` on `PATH`; web commands
used `/tmp/doublangu-node-v24.20.0/bin` on `PATH`.

- `test -z "$(gofmt -l internal cmd/doublangu-server)"` — passed.
- `go mod tidy -diff` — passed.
- `go test ./... -count=1 -buildvcs=false` — passed.
- `go test ./internal/reader ./internal/semantics ./internal/jobs ./internal/speech ./internal/workers ./internal/media ./internal/httpapi ./internal/store ./cmd/doublangu-server -count=1 -buildvcs=false` — passed.
- `go test -race ./internal/reader ./internal/semantics ./internal/jobs ./internal/speech ./internal/workers ./internal/media ./internal/httpapi -count=1 -buildvcs=false` — passed.
- `npm --prefix web run validate:openapi` — passed.
- `npm --prefix web run generate:api` twice — passed; generated output remained unchanged at SHA-256 `74db595e4e1562560b18509ec6144b14e045702b7ffe5f69a546f7c648cd1b58`.
- Standalone Ajv compilation of both JSON Schema contracts — passed.
- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 66 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 8 tests passed.
- `make verify` — passed with the temporary Git `safe.directory` environment override required by this checkout's ownership guard.
- `DOUBLANGU_TEST_CODEX_LIVE=1 go test ./internal/annotator -run Live -count=1 -v` — passed.
- `git diff --check` — passed.

## 2026-08-31 — Audible article reader backend and web handoff

- Implemented migration 005 for durable reader analysis/audio jobs, semantic
  items and senses, layered sentence/occurrence spans, speech units and
  profiles, render/blob references, narration manifests, and speech-worker
  enrollment state.
- Added the strict `reader.analysis.v2` Codex app-server boundary with
  content-hash caching, server-owned deterministic persistence, lease-aware
  background execution, reanalysis, and the legacy enrichment compatibility
  route.
- Added the frozen `speech-worker.v1` HTTPS contract, capability-scoped leases,
  heartbeats, idempotent multipart completion/failure handling, validated AAC
  media publication, authenticated range/HEAD serving, and narration retention,
  clear, and regeneration APIs.
- Added the readable Svelte reader with exact UTF-16 source reconstruction,
  sentence and multi-span construction highlighting, semantic learning state,
  hover/focus/click translation details, lexical hover pronunciation, lazy
  narration controls, themes, responsive layout, and actionable loading/error
  states.
- The separate macOS companion daemon and browser-beta deployment are not part
  of this repository; no real-Mac companion or public-beta acceptance is
  claimed by this handoff.

### Verification

All Go commands below used `/tmp/doublangu-go1.26.5/bin` on `PATH`; web commands
used `/tmp/doublangu-node-v24.20.0/bin` on `PATH`.

- `test -z "$(gofmt -l internal cmd/doublangu-server)"` — passed.
- `go mod tidy -diff` — passed.
- Focused normal Go tests for reader, semantics, jobs, speech, workers, media,
  HTTP API, store, and server — passed.
- Focused Go race tests for reader, semantics, jobs, speech, workers, media,
  and HTTP API — passed.
- `npm --prefix web run validate:openapi` — passed.
- `npm --prefix web run generate:api` twice — passed; output was byte-identical
  on the second generation (`74db595e4e1562560b18509ec6144b14e045702b7ffe5f69a546f7c648cd1b58`).
- Standalone Ajv compilation of both JSON Schema contracts — passed.
- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 65 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 8 tests passed.
- `make verify` — passed with a temporary Git `safe.directory` environment
  override required by this checkout's ownership guard.
- `DOUBLANGU_TEST_CODEX_LIVE=1 go test ./internal/annotator -run Live -count=1 -v` — passed.
- `git -c safe.directory=/root/code/doublangu diff --check` — passed.

## 2026-08-31 — Learner shell and enrichment recovery

- Replaced the unstyled implementation navigation with a readable dark learner
  shell focused on Articles and Paste article; removed Library, Settings, and
  Plugins from the primary product navigation.
- Added a root application-session gate so direct protected URLs redirect to
  sign-in before rendering, preserve their return target under `/beta`, and
  expose an explicit sign-out action.
- Clarified on the login page that deployment Basic Auth and the Doublangu owner
  session are distinct layers.
- Fixed the Dutch-to-English direction in the article flow. The retained
  experimental library form now uses valid BCP-47 choices and explains that it
  is not needed for article reading.
- Mapped persisted enrichment failure codes to meaningful recovery messages and
  server logs. Live inspection identified the reported article failure as
  `v1.enrichment_timeout` for a 2,099-character article while Codex remained
  authenticated.
- Extended the Codex enrichment deadline from two to five minutes; deployment
  proxy timeouts were raised to remain above the application boundary.
- Verification passed: `make verify`, focused Go tests and race tests, stable
  generated OpenAPI output, 57 web unit tests, 26 Chromium E2E tests, and the
  authenticated Codex live smoke. A fresh live Chromium session then proved
  direct-route login gating, dark readable colors, learner-only navigation, the
  timeout explanation, and successful retry of the exact previously failed
  article with no page errors or failed requests.
- Deployed beta release
  `6120e21e06129108a8da0ff4c6353e744120fe1c30b0bb3cbbe7b803798585be`.
  The 2,099-character article is now `ready` with English shadows; production
  remains unchanged.

## 2026-08-30 — Plugin-session 401 UX fix

- Changed UI-contribution loading so an absent or expired owner session routes
  to `/login` instead of exposing `UI contributions request failed: 401`.
  Non-authentication failures remain visible for diagnosis.
- Added unit coverage for 401 navigation and retained 503 errors, plus browser
  coverage for the unauthenticated Plugins route. Repaired the existing plugin
  E2E fixture boundary so its dynamic modules come from deterministic local
  assets instead of an unauthenticated backend request.
- Verification passed: generated API unchanged; Svelte check clean; 54 unit
  tests; five plugin and three reader browser tests; focused Go and race tests;
  authenticated Codex live test. `make verify` reaches only the pre-existing
  Git-fingerprint tests, which cannot pass because this salvaged directory has
  no usable `.git` repository.
- Deployed beta release
  `6bc9a0d23cf3d0057b351d71fe11437a748d466a89b55ab6f867d127c25a0f87`.
  Live Chromium proved 401 → `/beta/login`, then authenticated 200 on Plugins,
  with no raw alert, page error, or failed request.

## 2026-08-30 — Live beta Codex annotation

- The isolated `https://nlrn.evren.io/beta/` service now uses a checksum-pinned
  Codex CLI `0.151.0` ARM64 runtime with ChatGPT device authentication owned by
  the `doublangu-beta` service account.
- Live Chromium saved and enriched a Dutch article, rendered six interactive
  annotations and four visible English shadows, opened the hover detail dialog,
  and reported zero page errors or failed requests.
- Production remains absent and unchanged; migration still requires explicit
  owner approval.

## 2026-08-30 — `/beta` deployment path support

- Added a build-time SvelteKit base path and routed application links,
  navigation, API/CSRF, health, plugin settings/events, and plugin asset URLs
  through it while preserving root-based local development.
- Built and browser-tested the isolated live deployment at
  `https://nlrn.evren.io/beta/`; Chromium completed owner login, a reversible
  API/database mutation, settings diagnostics, and reader navigation without
  page errors or resources escaping the prefix.
- The initial deployed beta used an isolated database/data/media boundary and
  kept annotation disabled until the subsequent pinned Codex installation and
  service-account login completed.

### Verification

- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit` — 52 tests passed.
- `DOUBLANGU_WEB_BASE_PATH=/beta npm --prefix web run build` — passed.
- Live HTTP acceptance — unauthenticated `401`; authenticated shell, asset,
  health, and owner-session boundary passed.
- Live Chromium acceptance — passed with zero page errors.

## 2026-08-30 — Reader popover review fixes

- Pinned annotations now ignore hover/focus attempts on other annotations while
  deliberate clicks can repin the popover. A `ResizeObserver` coalesces
  remeasurement after Explore/detail/feedback content changes so the popover
  remains inside the viewport.
- Added unit coverage for pinned identity and browser coverage for cross-
  annotation hover plus the 320x720 content-resize bounds.

### Verification

- `npm --prefix web run check` — passed with 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 52 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 3 tests passed.
- `make verify` — passed with the checkout marked safe for Git subprocesses;
  the initial bare invocation was blocked by Git's dubious-ownership guard.
- `git -c safe.directory=/root/code/doublangu diff --check` — passed.

## 2026-08-30 — Article hover-shadows MVP

### Decisions

- Added persistent article, paragraph-block, annotation, and learning-state
  storage in migration 004. Source ranges are browser UTF-16 offsets; learning
  identity is NFC + Unicode case-folded text with collapsed Unicode whitespace.
- Kept the article surface conventional: passive English shadows are limited by
  density, phrase/group spans win over component words, and learned state hides
  only the shadow while keeping the annotation interactive.
- Added a strict injected annotator boundary. Production launches the installed
  `codex-cli 0.149.0-alpha.4.1` app-server in an ephemeral read-only/no-tool
  thread; disabled mode is explicit and returns `v1.annotator_unavailable`.
- Generated the app-server schema bundle in a temporary directory and based the
  private wire structs on the v2 `InitializeParams`, `ThreadStartParams`,
  `TurnStartParams`, `ItemCompletedNotification`, `TurnCompletedNotification`,
  and JSON-RPC envelope schemas. The generated bundle is not checked in.
- The app-server rejected `uniqueItems` in its response-schema dialect during
  the first live probe, so distinct alternatives are enforced locally and the
  unsupported keyword is omitted from the runtime schema.
- The live acceptance pass found Svelte template whitespace appearing between
  an annotated run and adjacent punctuation. The run template now concatenates
  annotated and plain text exactly, with a DOM reconstruction regression test.

### Verification

- `test -z "$(gofmt -l internal/reader internal/annotator internal/httpapi cmd/doublangu-server)"` — passed.
- `go mod tidy -diff` — passed with no module changes.
- `go test ./internal/reader ./internal/annotator ./internal/httpapi ./internal/store ./cmd/doublangu-server -count=1` — passed.
- `go test -race ./internal/reader ./internal/annotator ./internal/httpapi -count=1` — passed.
- `npm --prefix web run validate:openapi` — passed.
- `npm --prefix web run generate:api` — passed; a pre/post byte comparison was
  identical. The literal branch-base `git diff --exit-code` remains nonzero
  because the newly generated MVP client is intentionally uncommitted.
- `npm --prefix web run check` — passed with 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 51 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 3 tests passed.
- `make verify` — passed.
- `DOUBLANGU_TEST_CODEX_LIVE=1 go test ./internal/annotator -run Live -count=1 -v`
  — passed against the authenticated ChatGPT Codex app-server.
- `git -c safe.directory=/root/code/doublangu diff --check` — passed.

Clean-database browser acceptance also passed with the real owner login and
Codex enrichment: the Dutch source was preserved exactly; the manual fixture
produced 4 annotations and 2 passive shadows; word and group hover details,
pin/Escape, Meaning/Usage/Parts, learned-state persistence, and a contained
320x720 touch popover were all verified.

### Follow-ups

Audio/TTS, URL and ebook ingestion, speech/alignment, spaced repetition,
offline synchronization, mobile packaging, and alternate providers remain
outside this MVP.

## 2026-07-28 — Checkpoint 5: Native Loader Preflight Boundary

### Decisions

- **Nine typed failures only**: malformed JSON and manifest validation now map to
  `schema`; there is no separate parse stage. `complete` is success only.
- **Pre-open compatibility**: the caller supplies host API/Go/role/fingerprint
  values. The loader compares all of them, plus inspected artifact GOOS/GOARCH,
  before `plugin.Open`; it recognizes only Linux ARM64 ELF and macOS ARM64 Mach-O.
- **Observed evidence**: injected opener, native lookup, plugin manifest,
  comparator, and registrar collaborators append the test trace. Loader results
  contain typed stages/codes, not synthesized call counters.
- **Panic and nil containment**: typed-nil direct/inner plugin symbols and nil
  native plugins fail deterministically; post-open collaborator panics become
  their owning stage.
- **Zero-plugin smoke**: `/health` has deterministic core/loader/schema,
  unique-plugin, and registration-count fields. The Make target builds and starts
  the binary on an ephemeral loopback address, requests `/health`, then interrupts
  it cleanly.

### Verification

All verification commands pass:
- `go test ./internal/plugins -run 'Loader|PreOpen|Symbol|Subprocess|Diagnostic' -count=1`
- `go test -race ./internal/plugins -run 'Loader|Registration' -count=10`
- `go test ./cmd/doublangu-server -count=1`
- `make test-plugin-loader`
- `make test-core-no-feature-plugins`
- `make verify`

## 2026-07-29 — Checkpoint 6: Release Fingerprint And Real Artifact Integration

### Decisions

- **Canonical graph**: Module fingerprints retain every available main-module,
  dependency, and replacement path/version/sum field, sort on all canonical
  fields, and use `"no-deps"` only when no usable module data exists.
- **Final-artifact reporting**: `pluginbuild` resolves revision only after the
  pre-build candidate is available, then reads the final binary and sidecar to
  construct its report. Invalid explicit revisions fail before any `go build`.
- **Checksum boundary**: The production loader excludes only the self-referential
  checksum from embedded/sidecar comparison; byte tampering stops at
  `StageChecksum` before inspection or native loading.
- **Load evidence**: The role matrix executes server-only/server, agent-only/agent,
  and multi-target on both hosts in isolated subprocesses. It also proves both
  role rejections occur before `plugin.Open`, and uses matching `-race` plugins
  instead of skipping native loads.
- **Cross-directory fallback**: Equivalent `-buildvcs=false` builds from two
  checkout directories retain equal fingerprints and use helper Git revision
  fallback while preserving final artifact/sidecar/report parity.

### Verification

All verification commands pass:
- `go test ./internal/plugins ./tools/pluginbuild -run 'Fingerprint|BuildConfig|Module|Revision|Target|Artifact|Integration|Comparator' -count=1`
- `go test ./internal/plugins -run 'TestIntegration_FullMatrix' -count=1 -v`
- `go test -race ./internal/plugins -run 'TestIntegration_FullMatrix' -count=1 -v`
- `go test -race ./internal/plugins -count=1`
- `make test-plugin-loader`
- No stale artifacts in `bin/plugins/`
- `make verify`

## 2026-07-29 — Checkpoint 7: Trusted Svelte UI Plugin Host

### Decisions

- **Single contract and validated adapter**: Runtime code imports
  `contracts/ui-plugin-v1.ts` through `$contracts`. The Go snapshot endpoint
  emits exact `v1` snake_case JSON; the browser rejects malformed, duplicate,
  unauthorized, or invalid-priority descriptors before import.
- **Concurrent bounded module lifecycle**: A default export must have the
  descriptor owner ID plus `mount` and `destroy`. The host publishes one shared
  in-flight record per `(plugin ID, source URL)`, so concurrent contributions
  invoke one loader. Per-handle registration scopes are removed independently;
  the module destroy hook runs once after the final pending or mounted owner is
  released, and rejected loads cleanly permit retry.
- **Failure isolation**: Svelte context is installed during provider creation,
  each contribution uses Svelte 5's native boundary for render errors, and
  asynchronous import/mount failures retain the shell and healthy siblings.
- **Shared command/navigation state**: Plugin-scoped context methods reject
  conflicts. The same command registry drives linear and radial controls and all
  owner state is removed during unload despite dispose/destroy errors.
- **Asset boundary**: The assembled Go route requires a non-nil authorization
  callback, restricts GET/HEAD files to a canonical root/prefix, and grants
  immutable caching only after a `v1/<sha256>` URL matches the served bytes.
  The temporary server policy is explicitly allow-all until CP8 sessions exist.

### Verification

Repair-overlay verification is recorded in the active plan. It covers focused
Go/race checks, frontend check/unit tests, four real-browser lifecycle tests,
the repository verifier, artifact absence, and diff/budget checks.

### Sample Fixtures

Three content-addressed production-path fixtures under `web/static/plugin-assets/`:
one healthy multi-surface module, one throwing module, and one malformed module.

## 2026-07-28 — Checkpoint 2: Plugin Manifest And Schema Contract

### Decisions

- **Module path**: `doublangu` (matching CP1 branch convention).
- **Dual validation**: Go `Manifest.Validate()` and schema `ParsedSchema.Validate()`
  must agree on every fixture. The checked-in JSON Schema is the source of truth for
  field constraints; Go constants mirror it.
- **OneOf handling**: `source_revision` uses `oneOf` in the schema (`"unknown"` or
  7-64 hex). The internal parser handles both the `const` and `pattern` branches.
  When a property has `oneOf` without a top-level `type`, the parser treats it as
  a string validation.
- **Empty string vs null**: The schema rejects explicit `null` for any field (via
  type constraint and explicit null check in the required-field loop). Empty strings
  are rejected via `minLength: 1` on plain string fields or via pattern matching on
  fields with anchored regexes.
- **No network dependencies**: Schema validation uses a pure Go parser that reads
  the checked-in file; no `npx`, HTTP fetches, or floating downloads.
- **Canonical JSON**: `encoding/json` struct marshaling provides stable field
  ordering for `CanonicalJSON()`. Two manifests are equal iff their canonical JSON
  bytes match.

### Known Review Findings Incorporated

- Digit-leading SemVer prereleases (e.g. `1.0.0-0alpha.1`) are valid.
- Null-field errors name the specific field (e.g. `"id: must not be null"`).
- Whitespace-only metadata strings are accepted as valid (they satisfy `minLength: 1`).
- OneOf validation (source_revision) and array-item validation (target) have direct
  regression tests.
- Schema mutation sensitivity tests verify tests detect actual changes.

### Verification

All verification commands pass:
- `go test ./pkg/pluginapi/v1 ./internal/plugins -run 'Manifest|Schema' -count=1`
- No `npx` or `https?://` in scoped files
- `make verify`

## 2026-07-29 — Checkpoint 8 Repair: Secure Owner And SQLite Foundation

### Decisions

- **Browser login contract**: a public same-origin CSRF bootstrap issues the
  signed double-submit cookie before login; login/logout reject missing,
  mismatched, or tampered token pairs without mutating logout state.
- **Session and owner boundaries**: login atomically replaces a presented valid
  session; forced owner reset updates the password hash and revokes all sessions
  in one transaction. Owner action flags have no password values and use hidden
  TTY input or explicit stdin automation.
- **Trusted network/config boundary**: login throttling keys on `RemoteAddr`,
  not arbitrary forwarding headers. HTTPS public URLs cannot disable secure
  cookies, and startup creates the configured database parent.
- **SQLite and API evidence**: file databases enable WAL, foreign keys, and a
  5-second busy timeout. Injected migration sources prove upgrade preservation
  and transactional failure rollback. CP8 JSON failures share the versioned
  `error`/`code` envelope; CP7 plugin asset file responses remain non-JSON.

### Verification

The repair matrix covers assembled clean-cookie login, logout negative controls,
session rotation/reset rollback, forwarded-header spoofing, migration
upgrade/rollback/PRAGMA evidence, frontend type/behavior tests, module tidiness,
race checks, and the repository Make targets.

## 2026-07-29 — Checkpoint 9: Core Library Representation And Schema Contract

### Decisions

- **Production record boundary**: Constructors generate each own ID and call
  reusable record validators. Validators strict-parse own and parent IDs,
  canonicalise library/edition BCP-47 fields, and reject negative or reversed
  chapter/source millisecond ranges. Public struct literals remain possible for
  decoding and scanning, so callers must validate them before use.
- **ULID identity**: `newULID` uses `crypto/rand` to generate canonical
  uppercase ULIDs, with no cross-call monotonic-ordering guarantee. `ParseULID`
  rejects lowercase, whitespace-padded, wrong-length, and UUID-shaped (36-char
  dashed) values via `ulid.ParseStrict` plus an explicit uppercase check.
- **BCP-47 canonicalisation**: `ParseBCP47` wraps `golang.org/x/text/language`
  and validators retain its canonical output while rejecting empty,
  whitespace-padded, malformed, and undetermined tags. CP10 owns persistence of
  validated canonical metadata.
- **Integer-millisecond timing**: `Chapter` and `SourceAsset` use `int64`
  `start_ms`, `end_ms`, and `duration_ms`; validators and migration CHECK
  constraints enforce `>= 0` and `end_ms >= start_ms`.
- **Migration 002**: Five tables (library, work, edition, chapter, source_asset)
  with cascading foreign keys. The upgrade test applies checked-in 001 then the
  embedded migrations, while rollback derives a failing variant from checked-in
  002 and proves a clean retry.
- **Dependency pinning**: `github.com/oklog/ulid/v2` and `golang.org/x/text` are
  promoted to direct dependencies. The existing `google/uuid` indirect dependency
  is not referenced by any production or test code in the library/store scope.

### Verification

All verification commands pass:
- `test -z "$(gofmt -l internal/library internal/store)"`
- `go mod tidy -diff`
- `go test ./internal/library ./internal/store -run 'ULID|BCP47|Milliseconds|Migration|Rollback' -count=30`
- No forbidden patterns (`google/uuid`, `uuid.NewString`, `start_offset`, `end_offset`, `duration_seconds`, `float64`) in scoped files
- `git diff --check && make verify`

## 2026-09-01 — macOS Speech Worker Implementation

### [1] Local Mac implementation

- Added the native arm64 macOS 14+ menu-bar worker under
  `macos/speech-worker`, with private Application Support/Cache/Logs state,
  Keychain credentials, opt-in login launch, bounded private logs, and a
  one-job lease loop.
- Added Xander AVSpeech word/phrase rendering, AVFoundation mono 24 kHz AAC-LC
  postprocessing, strict protocol models/client, journaled upload spool, and
  cancellation-aware heartbeat/recovery behavior.
- Added an on-demand loopback Chatterbox child with exact process identity
  receipts, direct relocatable Python execution, bounded backoff, cancellation,
  and 600-second idle unload.

### [2] Pinned local voice/runtime evidence

- The supplied `voice_nl.flac` was converted to the private canonical reference
  WAV at 24 kHz mono PCM; its SHA-256 is
  `1dd25cc2ea1aa8314af2ce2f062eb44beeb662482516177e098f58f6b6ce10f5`.
- The bundled runtime is arm64 CPython 3.12.11 with `mlx-audio` 0.4.7, MLX
  0.32.2, and a checked-in `uv.lock`. Chatterbox is pinned to model revision
  `03565773edd72e949572557597af8063bb49a18a` and tokenizer revision
  `e0c9886f0e1c35ae85b1f27277416fb1`.
- The pinned model was prepared in the user-scoped model/cache paths and its
  exact model/tokenizer tree receipt was written privately. The stable model
  tree digest is `b5eeb1421a3da22aff80808a4f9ca0ccb0a6ce388965eaf53db9c6c4232e98ce`.
  No model or reference audio is included in the app or DMG.

### [3] Verification boundaries

- Focused Go correction tests pass for real profiles, active cancellation, and
  preferred lexical selection; the native Swift tests, format lint, runtime
  relocation/import probe, arm64 app verification, and development DMG
  verification pass locally on the target Mac. OpenAPI validation and generated
  API parity pass, and the web check, 65 unit tests, and 28 reader E2E tests
  pass.
- Opt-in live Chatterbox generation, idle-unload, and Xander AVSpeech buffer
  rendering tests pass locally; the child PID disappears after unload. The
  repository `make verify` target still stops at its pre-existing plugin
  integration module-graph mismatch (`host` versus `sidecar` fingerprints).
- Beta server integration was not used to claim beta acceptance in this pass.
- No beta deployment, enrollment, lease, authenticated media URL, login/reboot
  acceptance, network-loss acceptance, or owner publication was performed.

## 2026-09-01 — Speech Worker Review Repairs

### [1] Focused corrections

- Lease validation now accepts normal lowercase ASCII SHA-256 digests containing
  digits as well as `a`-`f`.
- Accepted Dutch regional source tags such as `nl-NL` are canonicalized to
  `nl` before speech units and jobs are created, keeping queued payloads
  leaseable by the single Dutch worker profile.
- Startup recovery retries unresolved rendering journals with backoff and does
  not acquire a new lease while recovery is unavailable.

### [2] Verification

- The focused Swift suite passes: 23 tests, 4 opt-in live tests skipped, 0
  failures; the new mixed-digit lease and offline-recovery-gate tests pass.
- Focused Go speech/worker tests pass, including the regional Dutch queue and
  lease test. Formatting and `git diff --check` pass.
