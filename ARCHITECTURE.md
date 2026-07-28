# Architecture

## Plugin Manifest Contract (Checkpoint 2)

### Manifest Structure

The native plugin manifest is a strict JSON document validated against
`contracts/plugin-manifest-v1.schema.json` (JSON Schema draft-07). The Go type is
defined in `pkg/pluginapi/v1/manifest.go`.

**Required fields:**

| Field | Type | Constraint |
|---|---|---|
| `id` | string | `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,126}[a-zA-Z0-9]$` |
| `version` | string | Strict SemVer 2.0.0; digit-leading prereleases allowed |
| `api_version` | string | Must equal host `APIVersion` constant (`v1`) |
| `go_version` | string | Must equal host `GoVersion` constant (`go1.26.5`) |
| `target` | array | Non-empty, unique, values from `["server", "agent"]` |
| `source_revision` | string | Either `"unknown"` or 7–64 ASCII hex characters |
| `artifact_checksum` | string | 64 lowercase hex characters (SHA-256) |
| `build_settings` | string | Canonical sorted build-settings hash |
| `module_graph` | string | Canonical module-graph hash |
| `name` | string | Human-readable plugin name (min 1 char) |
| `description` | string | Human-readable description (min 1 char) |
| `author` | string | Plugin author (min 1 char) |

**Additional properties are rejected.**

### Dual Validation

Every manifest is validated through two paths that must agree:

1. **Go validation** (`Manifest.Validate()`): typed struct validation in
   `pkg/pluginapi/v1`.
2. **Schema validation** (`ParsedSchema.Validate()`): rules extracted from the
   checked-in JSON Schema in `internal/plugins/manifest.go`.

A parity test in `internal/plugins/manifest_test.go` proves both paths accept and
reject the same fixtures. Schema constants (required fields, target enum, patterns)
are cross-validated against Go production constants without downloading tools.

### Canonical JSON

`Manifest.CanonicalJSON()` produces compact, stable output via `encoding/json`
struct marshaling. Two semantically identical manifests produce identical bytes.
`CanonicalEquals()` compares manifests by their canonical JSON representations.
