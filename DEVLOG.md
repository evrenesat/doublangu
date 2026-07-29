# Development Log

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
