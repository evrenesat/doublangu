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
