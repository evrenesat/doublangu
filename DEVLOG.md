# Development Log

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
