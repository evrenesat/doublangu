# Architecture

## Reliable Article Analysis and Audible Reader

The article reader is backed by the legacy article tables from migration 004
plus the additive semantic, job, speech, worker, and media-reference tables in
migration 005 and the analysis settings/history/cache tables in migration 006.
Legacy `article_annotation` and surface-keyed `learning_state` records remain
readable for compatibility; the v2 reader writes semantic-sense learning state
and does not infer new homonym identities from legacy rows.

The request and processing flow is:

1. Svelte `/reader/new` sends the title, exact body, and BCP-47 language tags to
   `POST /api/v1/articles`.
2. One transaction preserves the source blocks, computes the versioned
   content hash, and queues `reader.analysis.v2`. The API returns `201`; no
   browser request stays open for provider work.
3. The server snapshots the owner-selected model and reasoning effort into the
   durable job. It first checks an exact whole-article cache. On a miss, it
   prepares deterministic Unicode word tokens and UTF-16 source anchors, loads
   only local-lexicon candidates used by the article, and analyzes each source
   paragraph in its own isolated read-only/no-tools Codex app-server process.
   Each chunk receives only its paragraph, relevant candidates, and validated
   carry-forward senses from earlier paragraphs; its strict schema is generated
   from the exact token IDs and candidates in that chunk.
4. The validator requires exact sentence/token/construction occurrences,
   complete token coverage, legal UTF-16 boundaries, safe bounded strings, and
   candidate-or-new-sense references. One corrective turn reuses the same
   dynamic schema. Valid chunks are cached independently, so a retry can reuse
   completed paragraphs while a fresh run bypasses both chunk and whole-article
   caches.
5. A successful analysis transaction stores the cache, reusable semantic
   items/senses, ordered sentences, layered occurrences/spans, sense-keyed
   learning state, and deduplicated speech requests. Failed attempts retain
   owner-authenticated run/turn diagnostics and stable public error codes; the
   article remains readable while speech is pending or unavailable.

The owner settings page obtains a hidden-inclusive model catalog from the
provider, retains the last good catalog briefly when refresh fails, validates
model/effort selections, and shows recent runs. Changed selections are rejected
while that retained catalog is stale, and the UI keeps the save action disabled
until discovery succeeds again. Run detail is owner-only and
contains bounded prompt, schema, completion, metadata, validation, and stderr
excerpts for troubleshooting. Raw analysis content is not written to normal
server logs. If no model is selected, analysis fails closed with
`v1.analysis_model_unavailable` while the server continues serving the library;
the model catalog endpoint separately reports provider discovery failures.
Environment model/effort values seed the singleton only; owner changes take
precedence on later restarts. This is a manual comparison surface rather than
an automatic quality scorer, and the private database grows with retained
prompts and responses until an owner removes the database.

At startup, article recovery also finalizes every `analysis_run` left in the
`running` state by an exited process as a failed run with
`v1.analysis_interrupted`, completion metadata, and preserved paragraph/turn
diagnostics. The related article is requeued so the durable worker can retry
the unfinished analysis.

Semantic identity is `semantic_sense.id`, not spelling. An occurrence points to
   one sense and carries its effective shadow and pronunciation identity. Every
   deterministic word token is represented. A contiguous phrase/idiom is a
   single group occurrence whose component token shadows are suppressed;
   discontinuous constructions keep ordered multi-spans and marker styling
   while their member word occurrences remain available. This layered model is
   rendered directly instead of flattening spans into a non-overlapping list.

The web reader reconstructs source text from stored spans without HTML
injection. Source wrappers remain inline and shadows occupy a reserved visual
band, so English labels do not drive Dutch line breaking. Hover/focus/click,
keyboard, touch, and narration can open the compact popover or focus a sentence;
focus reflow compensates the viewport delta. Midnight, paper, and high-contrast
themes are local settings, and the UI polls only nonterminal analysis/speech
states with visibility-aware backoff.

Speech is independent of article playback. `speech_unit` deduplicates exact
word, short-phrase, and sentence text plus context; immutable `audio_render`
rows identify the complete byte-affecting speech profile and request hash.
Word and short contiguous phrase requests use the visible Dutch source text
from the occurrence span with `tts.avspeech.v1`; IPA is retained only as
pronunciation metadata and context. This keeps lexical audio aligned with what
the reader displays. These renders use `lexical_permanent` retention. Sentence
requests use `tts.chatterbox.v3`, are
bound to an ordered `article_sentence_audio` manifest, and use
`article_narration` retention. There is no monolithic article audio file.

The server owns the speech-worker queue and media publication boundary. An
outbound macOS worker enrolls with a one-time secret, authenticates with a
separate credential, long-polls HTTPS leases, heartbeats, and uploads strict
mono 24 kHz AAC-LC M4A metadata plus bytes. The server validates the live lease,
request hash, digest, signature, limits, and metadata, then commits the blob,
`audio_blob_reference`, render, and job outcome atomically. The browser reads
authenticated server audio URLs with strong ETags and single-range GET/HEAD;
it never reads a worker-local path. The macOS companion process is implemented
under `macos/speech-worker` from the root handoff
`macos-speech-worker-handoff.md`. It is a separate native Mac application
source tree; its private model, reference audio, credentials, logs, and upload
spool remain outside the repository app bundle.

Narration clearing cancels active long-form jobs, removes only article sentence
bindings, tombstones unreferenced narration renders, and runs crash-safe orphan
cleanup after commit. Lexical renders and blobs shared with another article are
never deleted. Regeneration reuses the same request identity. If no capable
worker is online, speech remains queued and the article reports
`waiting_for_worker`; analysis and the text reader do not fail.

Durable jobs use conditional SQLite leasing, 90-second leases, 30-second
heartbeats, dependency checks, bounded automatic backoff, and startup/periodic
recovery. Completion, failure, cancellation, and duplicate uploads are
idempotent for the job attempt and lease token. The server does not treat an
in-memory lock as the scheduling authority.

The provider boundary is `annotator.SemanticAnnotator`; the explicit disabled
mode returns `v1.annotator_unavailable`. Provider/model/prompt/contract
provenance is stored in the cache and sense rows, while raw reasoning and
provider diagnostics stay server-side. `/enrich` remains a documented `202`
compatibility alias for `/reanalyze` during this API version.

The SvelteKit client supports `DOUBLANGU_WEB_BASE_PATH`, and the shared path
helper prefixes navigation, API/CSRF, health, plugin routes, and audio URLs.
The root layout gates protected application routes through the owner session;
worker credentials are never browser credentials. Legacy library, plugin-host,
and diagnostics routes remain available for development but are not learner
navigation.

This slice deliberately excludes URL/ebook extraction, STT, forced alignment,
spaced repetition, multi-user accounts, offline/mobile packaging, alternate
providers, and the future audio-only lesson/session experience. Those features
can reference reusable senses, speech units/renders, and jobs later without
making an article their owner.

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

`pluginassets.New` requires an authorization callback. The server assembles the
owner-session policy, so UI contribution metadata and plugin assets are private.
The handler accepts only GET/HEAD regular files beneath a
canonical symlink-resolved root and mounted prefix. It deterministically returns
401/403/404/405 for authorization, escape, missing, and method controls. Only
`v1/<sha256>/<file>` URLs whose bytes match the digest receive immutable caching;
unversioned files are served with `no-store`.

## Owner Authentication And SQLite Foundation (Checkpoint 8)

`internal/config` accepts a required 32-byte-minimum decoded server secret,
validated listen/public URL values, database/media/data paths, and a bounded
session lifetime. Local loopback HTTP keeps secure cookies off by default; an
HTTPS public URL cannot override them off. Startup creates the configured
database parent rather than an unrelated data directory.

The browser obtains a signed, non-HttpOnly, strict-SameSite CSRF cookie from
`GET /api/v1/auth/csrf`. Login and logout require its matching
`X-CSRF-Token` header. Login uses the immediate remote peer for throttling,
never untrusted forwarding headers; it atomically replaces a valid presented
session and refreshes the CSRF cookie. A password reset updates the owner hash
and deletes all sessions in one transaction. Owner bootstrap/reset action flags
accept no values: passwords are hidden on a TTY or read from explicit stdin.

SQLite opens with WAL, foreign keys, and a 5-second busy timeout before applying
numbered transactional migrations. The migration runner exposes only a test
source seam, which proves older populated schemas retain data and failed DDL or
version records roll back together. CP8 JSON endpoints route all non-2xx
responses through `httpapi.APIError`, containing human `error` and a versioned
machine `code`; CP7 plugin asset responses intentionally retain their established
non-JSON file/error contract.

### Sample Fixtures

Content-addressed browser fixtures live under `web/static/plugin-assets/`: one
healthy module proves route, panel, settings, command, reader, and radial
contributions; the other fixtures throw during mount or have a malformed default
export. Browser tests exercise these assets through the production host and Go
asset route.

## Library Representation Contract (Checkpoint 9)

### Record Types

Five core library record types are defined in `internal/library/types.go`:

| Record | Table | Key identity |
|---|---|---|
| `Library` | `library` | ULID; groups works under `source_language`/`target_language` (BCP-47) |
| `Work` | `work` | ULID; belongs to library (FK cascade) |
| `Edition` | `edition` | ULID; specific format/version of a work (FK cascade) |
| `Chapter` | `chapter` | ULID; timed segment with `start_ms`, `end_ms`, `duration_ms` (int64) |
| `SourceAsset` | `source_asset` | ULID; source file record with timing (int64 ms) |

Constructors generate every record's own canonical 26-character uppercase ULID
through the unexported `newULID` boundary. Public DTO literals remain useful for
database scans and decoding, but must pass their record's `Validate` method
before use; validation strict-parses own and parent IDs, canonicalizes library
and edition language fields, and checks chapter/source timing. `newULID` uses
`crypto/rand` without a cross-call monotonic-ordering promise.

### ULID

The `ULID` type preserves an encoded DTO identity until validation in
`internal/library/ulid.go`. `newULID()` is the sole creation boundary, while
`ParseULID` enforces:

- Exactly 26 uppercase Crockford base32 characters (excludes I, L, O, U)
- No whitespace, no lowercase, no trailing/leading garbage
- UUID-shaped inputs (36-char dashed) are rejected as negative controls

### BCP-47 Language Tags

`ParseBCP47` in `internal/library/language.go` wraps
`golang.org/x/text/language`. Record validation canonicalizes valid tags (for
example, `"en-us"` → `"en-US"` and `"zh-hans"` → `"zh-Hans"`) and rejects:

- Empty strings
- Whitespace-padded input
- Malformed input (starts with digit, special characters, too short)
- Tags that parse to the root/undetermined language
- Canonical forms that are not round-trip stable

Checkpoint 10 will persist these validated canonical metadata values; Checkpoint
9 defines and proves the representation boundary only.

### Migration 002

`internal/store/migrations/002_library.sql` creates the five library tables with:

- Foreign keys with `ON DELETE CASCADE` throughout the chain
- `CHECK (start_ms >= 0)`, `CHECK (end_ms >= 0)`, `CHECK (duration_ms >= 0)`
- Table-level `CHECK (end_ms >= start_ms)` on `chapter` and `source_asset`
- Indexes on every foreign-key column

The migration runner applies it transactionally after 001_initial; a failing
migration rollback leaves no schema changes and no version record.
