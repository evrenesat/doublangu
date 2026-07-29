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

## Native Loader Preflight Boundary (Checkpoint 5)

### Pipeline Stages

The loader enforces a strict nine-stage pipeline. Every stage before `open` is a
**pre-open** stage — it must produce an empty downstream trace. Tests observe
calls made by injected collaborators, rather than loader-authored counters.

| Stage | Pre-open? | Description |
|---|---|---|
| `sidecar_read` | Yes | Read the sidecar JSON file from disk |
| `schema` | Yes | Require parsed schema; reject malformed, duplicate, unknown, null, and invalid manifests |
| `checksum` | Yes | SHA-256 of `.so` bytes must equal `artifact_checksum` |
| `compatibility` | Yes | Host API/Go/role/hashes and inspected artifact GOOS/GOARCH match exactly |
| `open` | No | Native `plugin.Open` via injected `PluginOpener` |
| `lookup` | No | `NativePlugin.Lookup("DoublanguPlugin")` |
| `symbol_contract` | No | Type-assert to `Plugin` or `*Plugin` |
| `embedded_equality` | No | Canonical embedded-manifest comparison with sidecar |
| `registration` | No | Atomic registration via `Registry.RegisterPlugin` |
| `complete` | N/A | Success |

### Injected Seam

The loader has injectable opener, artifact-inspection, manifest-comparison, and
registry-registration collaborators. Production inspection recognizes Linux
ARM64 ELF and macOS ARM64 Mach-O without executing candidate code. Callers supply
the exact build-settings and module-graph hashes; their derivation remains a
later fingerprint concern.

### Symbol Contract

The loader accepts exactly two `DoublanguPlugin` symbol shapes:
- A non-nil value implementing `v1.Plugin`; or
- A non-nil `*v1.Plugin` whose contained interface is non-nil.

All other shapes — including typed-nil concrete implementations — fail at
`symbol_contract`. Panics from open, lookup, manifest retrieval, comparison, or
registration become the corresponding typed failure result.

### Zero-Plugin Startup

The server (`cmd/doublangu-server`) starts with an empty registry and prints a
visible banner. `/health` reports independent core and loader readiness, checked
schema availability, sorted unique contributing plugin IDs, and a separate total
of registration surfaces. The server exposes no readiness endpoint.

## Fingerprint And Artifact Integration (Checkpoint 6)

### Canonical Fingerprint Model

Plugin compatibility is enforced through three fingerprints computed from the
artifact binary:

| Fingerprint | Source | Exclusions |
|---|---|---|
| Build Settings | `debug.BuildInfo.Settings`, sorted key-then-value | `vcs`, `vcs.*`, `-buildmode`, `-ldflags` |
| Module Graph | Main module and sorted dependencies, each with path/version/sum/replacement data | None (no usable module data → `"no-deps"`) |
| Artifact Checksum | SHA-256 of final plugin `.so` bytes | None |

Build Settings and Artifact Checksum are SHA-256 values stored as 64 lowercase
hex characters. Module Graph is also SHA-256 when module data is available, or
the explicit `"no-deps"` sentinel otherwise. Unknown non-VCS settings remain
significant and participate in the fingerprint.

### Two-Phase Build

The `tools/pluginbuild` tool uses a two-phase approach:

1. **Pre-build**: compile the plugin with placeholder fingerprint values and
   read its candidate `debug.BuildInfo`. This candidate supplies the
   `vcs.revision` precedence tier and the values injected into the final build.

2. **Final build**: rebuild with `-ldflags -X` injecting the computed hashes,
   source revision, and canonical target list. The helper then reads the final
   binary for its sidecar fingerprints and `ArtifactReport`; its checksum is
   written only to the sidecar because it is self-referential.

### Artifact Parity

The production loader defaults to the artifact parity comparator, which compares
embedded and sidecar manifests in every field except `artifact_checksum`. The
checksum is self-referential: embedding a file's SHA-256 inside the file changes
the file, making the embedded value instantly stale. The pre-open checksum
validation (`StageChecksum`) independently verifies file integrity against the
sidecar before inspection or native loading.

### Sidecar ↔ Embedded Parity

After loading, the plugin's `Manifest()` must match the sidecar in all fields
except `artifact_checksum`. The build tool injects the following via ldflags:

- `buildSettingsHash` — canonical build-settings hash
- `moduleGraphHash` — canonical module-graph hash
- `sourceRevision` — resolved source revision
- `targetJSON` — JSON-encoded canonical target array

### Host Role Validation

The loader validates the host role against the plugin's target list at the
`compatibility` stage, before native code is opened. A plugin targeting only
`["server"]` is rejected on an agent host, and vice versa. Multi-target
plugins (`["agent", "server"]`) load on either host.

The integration matrix builds matching `-race` plugins when the host test binary
uses the race detector. Each allowed or rejected role case runs in a subprocess,
so every process performs at most one real `plugin.Open` while proving all four
allowed loads and both pre-open role rejections.

### Source Revision Precedence

Source revision follows a four-tier fallback: explicit build-tool option →
`vcs.revision` from the pre-build candidate artifact → helper
`git rev-parse HEAD` → `"unknown"`.
Explicit revisions must be 7–64 ASCII hex characters; empty, whitespace,
control-character, and leading-dash values are rejected.

## Trusted Svelte UI Plugin Host (Checkpoint 7)

### Contracts

| Layer | File | Role |
|---|---|---|
| Backend registration | `pkg/pluginapi/v1/registration.go` | `UIRegistration` DTO with `SourceURL` |
| Read-only HTTP view | `internal/plugins/ui_snapshot.go` | deterministic `v1` snake_case contribution payload |
| Frontend contract | `contracts/ui-plugin-v1.ts` | sole TypeScript contract, imported as `$contracts` |
| Runtime host | `web/src/lib/plugins/UIHostV1.ts` | descriptor validation, module lifecycle, registries |

### Lifecycle

1. `/api/v1/ui/contributions` emits exact versioned snake_case registrations,
   which the host validates and adapts to camelCase before an import.
2. A source URL must resolve to the current origin beneath
   `/api/v1/plugins/assets/`; credentials, queries, fragments, traversal,
   protocol-relative, `data:`, and foreign-origin forms are rejected.
3. The imported namespace must default-export a module whose ID equals the
   descriptor owner. The host publishes one in-flight record before loading, so
   concurrent contributions for a `(plugin ID, source URL)` await one loader
   promise; a conflicting URL or failed waiter cannot replace sibling state.
4. Each contribution handle owns its exact command/navigation scope. Disposal
   removes that scope even if the handle throws, while the shared module destroy
   hook runs exactly once after the final pending or mounted contribution is
   gone. A rejected shared load leaves no host state and a later mount may retry.
   The Svelte provider installs context before rendering children;
   `PluginContainer` uses Svelte 5's `<svelte:boundary>` for render failures and
   explicit promise handling for import/mount failures.

The shared `CommandRegistry` rejects duplicate IDs. Its data drives both the
linear and radial command controls. Plugin contexts receive scoped registration
methods, SvelteKit navigation, URL-escaped settings/event paths, non-2xx write
errors, and frozen theme-token snapshots.

### Plugin Assets Serving

`pluginassets.New` requires an authorization callback. The current zero-plugin
server assembles an explicit temporary allow policy; CP8 replaces that policy
with owner sessions. The handler accepts only GET/HEAD regular files beneath a
canonical symlink-resolved root and mounted prefix. It deterministically returns
401/403/404/405 for authorization, escape, missing, and method controls. Only
`v1/<sha256>/<file>` URLs whose bytes match the digest receive immutable caching;
unversioned files are served with `no-store`.

### Sample Fixtures

Content-addressed browser fixtures live under `web/static/plugin-assets/`: one
healthy module proves route, panel, settings, command, reader, and radial
contributions; the other fixtures throw during mount or have a malformed default
export. Browser tests exercise these assets through the production host and Go
asset route.
