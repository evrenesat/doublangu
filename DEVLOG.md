# Development Log

## 2026-09-04 — Profile saves fixed server-side; editor model picker becomes a select

Fixed the "cannot use OpenRouter / create any profile" blocker reported from
the Analysis settings page (error shown: `bindings[0]:
provider_config_fingerprint is required`).

- Server (`internal/httpapi/pipeline_analysis.go`): `canonicalizeUsableBindings`
  validated bindings against the live registry but never filled the trusted
  snapshot identity, while `ProfileStore.validateProfile` → `ProfileSnapshot.
  Validate` requires it — so every POST/PUT `/api/v1/analysis/profiles` failed
  with that exact error. It now sets `ProviderType` and
  `ProviderConfigFingerprint` from the live provider descriptor, mirroring
  `usableProfileBindings`; contradicting client-declared types are still
  rejected. Existing Go tests had masked this by posting snapshot-shaped
  bodies that hardcoded `provider_config_fingerprint`.
- Server (`internal/analysis/profiles.go`): the store's provider-type gate now
  also accepts `mac_relay` (same oversight the frontend fix in 5aa0e2b
  addressed client-side); unknown types are still rejected.
- Web (`AnalysisPipelinePanel.svelte`): the profile editor's Model field now
  renders a `<select>` populated from the provider's catalog models —
  matching the provider test area — and falls back to the free-text input
  only when a provider advertises no catalog models. This removes the
  pre-filled `meta/muse-spark-...` text input the report asked to become a
  selectbox.
- Regression tests: `TestPipelineProfileCreateFillsSnapshotIdentityFromRegistry`
  posts browser-shaped 4-field bindings on create and replace (verified to
  fail with the exact reported error before the fix) and asserts the
  contradicting-type rejection; `TestProfileStoreAcceptsMacRelayBindings`
  covers the store gate; the mac_relay panel test asserts the editor model
  select.

Verification: `go build ./internal/...` clean; `go test ./internal/httpapi
./internal/analysis ./internal/pipeline ./internal/config` pass; `-race` on
the profile paths passes; `npm run check` (0/0), `npm run test:unit`
(130/130), `npm run build`, `git diff --check` — all clean.

## 2026-09-03 — Settings UI: mac_relay now uses the numeric stage-option schema

Fixed a frontend provider-type bug that survived the settings restructure:
the UI keyed the stage-option schema on `openai_compatible` only, so a
`mac_relay` provider was treated like Codex — the editor and test forms
offered a reasoning effort, and profile saves / tuple tests posted
`{"reasoning_effort":"low"}` which the server's strict option codec rejects
(`v1.validation_error`, shown as "Options are invalid for this provider.").
The backend (`internal/config/providers.go` `CanonicalizeProviderOptions`)
requires exactly `temperature_milli` + `max_output_tokens` for both
`openai_compatible` and `mac_relay`.

- Added `usesNumericStageOptions()` to `web/src/lib/settings/analysisProfiles.ts`
  as the single source for the numeric schema set and replaced all six
  hardcoded comparisons there (defaults, wire canonicalization, options
  validation, effort-error skip, blank/retested test tuples) plus the four
  in `AnalysisPipelinePanel.svelte` (assign-provider/model-change option
  resets, test-form fields, binding-editor fields).
- `mac_relay` bindings now default to `{temperature_milli: 0,
  max_output_tokens: 16384}`, render numeric controls in both the profile
  editor and the per-stage test area, and post numeric options to
  `/api/v1/analysis/providers/{id}/test`.
- Tests: `analysisProfiles.test.ts` grew a `mac_relay` provider fixture and
  coverage for the grouping helper, defaults, canonical wire options,
  range/non-integer errors, effort-free test tuples, and the tuple wire
  request; `AnalysisPipelinePanel.test.ts` gained an end-to-end case that
  expands a `mac_relay` provider, asserts numeric controls with no effort
  field, runs a tuple test posting numeric options, and checks the profile
  editor renders numeric binding fields.

Verification: `npm run check` (0 errors/warnings), `npm run test:unit`
(130/130), `npm run build`, `git diff --check` — all clean.

## 2026-09-03 — Settings restructure, analysis-runs pages, speech workers UI

Implemented `plans/in-progress/redesign.md` on `main`: Settings is now a
four-section shell (`/settings` reader preferences only, `/settings/analysis`,
`/settings/workers`, `/settings/system`), analysis-run history moved to
top-level `/analysis-runs` + `/analysis-runs/[id]` (old
`/settings/analysis-runs/[id]` route removed), and the global nav gained
`Analysis runs`. Details:

- `web/src/routes/settings/+layout.svelte` owns the Settings heading and
  local section nav (active section via `$page.route.id`, `aria-current`);
  the `More` block (Library/Plugins links) is gone; `/plugins` itself is
  untouched and simply has no ordinary link.
- `AnalysisPipelinePanel.svelte` reordered: active profile card → profiles →
  providers; per-stage conformance/test fixtures now live inside a native
  `<details class="provider-test">` (collapsed by default) per provider, with
  `Refresh catalog` beside it. Test semantics, validation, and CRUD unchanged.
- New `/settings/workers` page: one-time enrollment token generation
  (token kept in component memory only, copied via
  `navigator.clipboard.writeText`, regenerate replaces the shown token),
  worker list with capabilities/relay/last-seen, and revoke via inline
  confirmation naming the worker (no `window.confirm`).
- Client additions (`web/src/lib/api/client.ts`): `SpeechWorker`,
  `WorkerEnrollment` types and `listSpeechWorkers()`,
  `createSpeechWorkerEnrollment()`, `revokeSpeechWorker()` over the existing
  owner endpoints; no OpenAPI change (`npm run generate:api` diff verified
  empty).
- Reader links inside `ArticleReader.svelte` that point at profile/model
  configuration now target `/settings/analysis` instead of `/settings`.
- Tests: `settings-page.test.ts` rewritten for the reader-only page (incl.
  asserting no analysis/health requests), new tests for the settings layout
  nav, analysis-runs list (limit 25, Load more appends, provenance), run
  detail (new route, back link, 404 retry), speech workers (7 cases), system
  diagnostics (3 cases), and two `AnalysisPipelinePanel` cases (information
  order + collapsed provider tests, refresh + conformance run). E2E updated:
  diagnostics assertion moved to `/settings/system`, settings preference test
  no longer mocks analysis/health endpoints.

Verification (all from `web/` unless noted):

- `npm run check` — 0 errors, 0 warnings.
- `npm run test:unit` — 124/124 pass (21 files).
- `npm run build` — succeeds (adapter-static output written).
- `npm run generate:api` then `git diff src/lib/api/generated.ts` — empty.
- `npm run test:e2e` — 34/35 pass. One pre-existing failure on `main`
  unrelated to this work:
  `reader-preference.spec.ts › a saved off value survives a reload through
  the server setting` also fails with every change from this task stashed
  (verified against pristine `df1a16e` content). Not fixed here to keep the
  diff scoped.
- Repo `make verify` — vet, check-no-network, manifest, auth-foundation,
  login-ui pass; web unit tests pass; `test-fingerprint-integration`
  (`TestIntegration_FullMatrix`) fails on this machine for reasons unrelated
  to this task. Diagnosis, verified empirically: on this Go toolchain the
  main checkout always VCS-stamps `-buildvcs=true` artifacts with a main
  module pseudo-version (`v0.0.0-<time>-<rev>`, `+dirty` whenever untracked
  files exist — the two files in the repo root are enough), while the `go
  test` binary always reports `(devel)`, so `ComputeModuleGraphHash` can
  never match between host and plugin sides in the main checkout. The test
  passes in a linked `git worktree` (stamping is skipped there, both sides
  `(devel)`) — confirmed 3/3 at pristine `df1a16e`. Hiding untracked files
  (`status.showUntrackedFiles=no`) yields a no-dirty stamp and the test
  still fails, which decouples it from the owner's untracked files and from
  this task's content entirely (`go.mod`/`go.sum` untouched, `go list -m all`
  identical). Treated as a pre-existing environment-sensitive test; not a
  code regression.
- `git diff --check` — clean at commit time.

## 2026-09-03 — Mac LLM relay backend checkpoint 4: blocked (no beta deploy path, no v0.2 Mac app)

Checkpoint 4 (beta proof with the sibling app) was inventoried step by
step and cannot execute from here. Checkpoints 1–3 are preserved
unchanged: same uncommitted worktree, `go build ./internal/... ./cmd/...`
clean, `git diff --check` clean, so the recorded race verification stands.

Blockers, verified just now:

- Deploy (step 1): no deploy tooling exists in the repo (`tools/` holds
  only `pluginbuild`; the Makefile has no deploy target) and SSH to the
  beta host fails (`Host key verification failed`, no credentials). The
  checkpoint 1–3 work is also uncommitted on `main`, while prior beta
  releases pin reviewed commits — deploying this worktree would bypass
  review. No owner deployment approval was given.
- v0.2 Mac app (step 2): the sibling plan is still marked "Ready for
  implementation after backend checkpoints 1–2 are deployed to beta" and
  `macos/speech-worker` contains zero relay references
  (`llm_relay|llm\.relay|RelayCapability` match nothing). There is no v0.2
  app to install, pair, or re-enroll.
- Steps 3–9 need the owner Mac (`dev-ren-mac`), Mac-local OMLX, and owner
  Settings/Keychain actions. This container is Linux (`Linux codex
  6.17.2-1-pve x86_64`); `http://127.0.0.1:8899/v1/models` refuses
  connection here, and OMLX must stay bound to the Mac's loopback anyway.
- Public surface (step 10) baseline, read-only probe of the current
  (pre-relay) beta: `GET /` → 302, `GET /beta/` → 401,
  `GET /beta/api/v1/speech-worker/lease` → 401 (worker-auth enforced, no
  browser-session access). No relay route exists to check yet; re-run this
  probe after a real deploy of checkpoints 1–3.
- Rollback (§10 item 11): not performed — nothing was deployed. The exact
  procedure (remove the `mac_relay` entry, fail closed; stop the relay
  lane, TTS continues; migration 011 forward-only) stands as recorded.

To unblock: review + commit checkpoints 1–3, deploy the resulting commit
to beta through the owner's normal beta procedure, implement the sibling
Mac plan, then re-run checkpoint 4 steps 2–10 from the owner Mac.

## 2026-09-03 — Mac LLM relay backend (checkpoints 1–3; checkpoint 4 pending beta)

Implemented `plans/in-progress/mac-llm-relay-backend-handoff-reviewed.md`
checkpoints 1–3 on `main`. Checkpoint 4 (beta proof with the sibling Mac
app) needs the owner Mac and beta deployment and is not attempted here.

- Checkpoint 1: migration `011_llm_relay.sql` (job rebuild admitting
  `llm.relay.v1`, dependency backup/restore, relay worker columns,
  `llm_relay_result`), `jobs.LLMRelayJobType` + spec validation,
  `jobs.Store.RecoverExpiredJob`, and the new `internal/llmrelay` package
  (strict request/result validation, request hashing, enqueue + 250 ms
  durable wait, availability query, atomic completion). Migration number is
  011, not 009: the baseline has since grown 009 (stage cache identity)
  and 010 (truncation flags); `db_test.go` version/count assertions moved
  10 → 11. The plan's `idx_job_dependency_reverse` name does not exist;
  the rebuild recreates the real 005 index
  `idx_job_dependency_dependency`.
- Checkpoint 2: relay lane in the worker protocol (enroll/lease
  `llm_relay_capabilities`, relay-only presence, relay lease responses,
  three-part completion, relay fail matrix, concurrent TTS + relay
  leases), `contracts/openapi.yaml` extension, regenerated
  `web/src/lib/api/generated.ts` with no hand edits, v0.1 worker
  backward-compatibility proof.
- Checkpoint 3: `mac_relay` provider type in config + annotator (narrow
  `RelayExecutor`, history-preserving session, provider-timeout scope,
  auth/unreachable/model-unknown/expiry → unavailable,
  invalid-response → invalid-output, deadline → timeout, caller cancel
  preserved), shared relay service assembly in `main.go`, and a valid
  current-schema `config/provider.example.json` including `mac-omlx`.

### Verification

- `go test ./internal/... -race -count=1` — all 17 packages passed.
- `go test -race ./internal/annotator ./internal/config ./cmd/doublangu-server`
  — passed; `DOUBLANGU_TEST_CODEX_LIVE=1` live app-server smoke — passed
  (`TestLiveCodexChunk` skips: `DOUBLANGU_TEST_CODEX_MODEL` unset).
- `npm --prefix web run validate:openapi` + `generate:api` (twice,
  stable) — passed.
- `npm --prefix web run check` — 0 errors, 0 warnings;
  `test:unit -- --run` — 103 passed (17 files).
- Reader E2E (`test:e2e`, needs Go on PATH for its webServer) — 35 passed.
- `make verify` — passed. `gofmt`, `git diff --check` — clean.
- Handoff evidence recorded here per §10 items 1–6, 10–11; items 7–9
  (catalog models, stage provenance, OMLX-offline/retry) need checkpoint 4
  beta hardware. Rollback: remove the `mac_relay` entry (fail closed),
  stop the relay lane (TTS continues); migration 011 is forward-only.

## 2026-09-03 — Pipeline third review: compat registry, seed validation, input schema, exact options, failure caching

Addressed five new material findings from `plans/reviews/latest_review.md`:

- Compatibility mode now synthesizes `codex-app-server` from legacy
  environment (including the disabled state) and imports a nonblank legacy
  selection as `Imported Codex`; `run()` always uses `PipelineRunner` and
  configures the article handler. An explicit file combined with legacy
  provider-selection env is a startup error.
- Bootstrap seeding discovers each referenced model via a live catalog and
  validates Codex model/effort pairs before insert/activation; discovery
  failures log provider IDs only and leave profiles empty.
- `AnalysisProfileInput` uses a dedicated `AnalysisProfileBindingInput`
  (four write fields); validity state stays response-only. Added GET-to-PUT
  round-trip tests on both sides of the contract.
- OMLX numerics travel exactly as configured; `stageOptionsError` disables
  Save/Test with explicit range messages, and the server remains the strict
  authority. Removed the silent clamp (and its unit test).
- The catalog retains failed attempts (including cold failures) for the TTL
  window, serves stale/error state without redialing, coalesces concurrent
  per-provider refreshes onto one call, and keeps `refresh=true` as the
  explicit bypass. New call-counting tests cover suppression, expiry retry,
  and single-call coalescing.

### Verification

- Focused Go tests (8 packages incl. cmd) — passed.
- Race tests (semantics, annotator, analysis, reader, httpapi) — passed.
- `go test ./... -count=1 -buildvcs=false` and `make verify` — passed.
- OpenAPI validation + double generation byte-identical (`cmp`) — passed.
- `npm --prefix web run check` — 0 errors, 0 warnings.
- `npm --prefix web run test:unit -- --run` — 97 passed (15 files).
- Reader E2E and opt-in live provider tests — skipped (container Chromium
  crashes before app load; no credentials supplied).
- `gofmt`, `go mod tidy -diff`, `git diff --check` — clean.

## 2026-09-03 — Pipeline follow-up review: tuple validation, runs, effort init, validity, cache provenance

Addressed five new material findings from `plans/reviews/latest_review.md`:

- Conformance tests validate the complete model/options tuple against the
  shared catalog before any provider call: blank models, non-object options,
  unlisted models, and unsupported efforts are 400s, stale/unavailable
  catalogs are 503s, and only executed fixtures are retained. `model_id` is
  now required in OpenAPI.
- Recent runs render in both modes with profile plus both compact bindings
  (`bindings` derived server-side from the stored profile snapshot;
  `AnalysisRunBinding` schema); legacy rows keep the model/effort fallback.
- The profile editor initializes a new binding's effort to the first effort
  its model advertises (incomplete when none is), resets it on provider/model
  change only when unoffered, preserves stored efforts without coercion, and
  blocks Save with the mismatch reason via `bindingEffortError`.
- Profile reads carry per-binding `valid`/`validity_reason` derived from the
  live registry and shared catalog; Settings shows the reason beside the
  affected binding and disables activation for known-invalid profiles while
  editing stays available.
- Stage-cache writes store the producing run ID (`run.ID`) instead of `""`;
  hits retain that provenance. Extended the cache integration test to assert
  every cache row points at the producing run.

### Verification

- Focused Go tests (8 packages incl. cmd) — passed.
- Race tests (semantics, annotator, analysis, reader, httpapi) — passed.
- `go test ./... -count=1 -buildvcs=false` and `make verify` — passed.
- OpenAPI validation + double generation byte-identical (`cmp`) — passed.
- `npm --prefix web run check` — 0 errors, 0 warnings.
- `npm --prefix web run test:unit -- --run` — 95 passed (15 files).
- Reader E2E and opt-in live provider tests — skipped (container Chromium
  crashes before app load; no credentials supplied).
- `gofmt`, `go mod tidy -diff`, `git diff --check` — clean.

## 2026-09-03 — Pipeline review feedback: shared catalog, gated legacy editor, per-tuple conformance

Addressed five material review findings on the configurable analysis provider
pipeline (`0cfd571`):

- `ProviderCatalogService.Snapshot` builds the cached snapshot while holding
  the mutex and copies the models slice, closing the race between cached reads
  and concurrent provider refreshes. New `-race` regression test fails on the
  old code (`DATA RACE`) and passes on the fix.
- `main.go` constructs one shared catalog and injects it into both
  `ConfigurePipeline` and `NewPipelineAnalysisHandler`, so a provider refresh
  in Settings is immediately visible to article/fresh-profile validation.
- The Settings legacy model/effort editor now renders only in compatibility
  mode (no configured providers); pipeline mode shows the provider-profile
  heading instead. A failed providers probe fails open to the legacy surface.
- `GET /api/v1/analysis/providers` declares `refresh`/`provider_id` in
  OpenAPI; the web client sends them and each provider card has a Refresh
  catalog button that bypasses the five-minute cache for that provider only.
- Conformance tests run per stage/model/options tuple (model select plus
  per-type options per stage) instead of one hardcoded linguistic fixture.
  `ServeProviderTest` retains the latest in-memory result per tuple and the
  providers listing returns those summaries, so results survive reload; no
  article, job, run, cache, or database row is created for a test.

### Verification

- Focused Go tests (config, semantics, annotator, analysis, reader, httpapi,
  store, cmd) — passed.
- Race tests (semantics, annotator, analysis, reader, httpapi) — passed,
  including the new catalog race test.
- `go test ./... -count=1 -buildvcs=false` and `make verify` — passed.
- OpenAPI validation + double generation byte-identical (`cmp`) — passed.
- `npm --prefix web run check` — 0 errors, 0 warnings.
- `npm --prefix web run test:unit -- --run` — 93 passed (15 files), including
  new tuple-helper, provider-refresh client, and pipeline-mode page tests.
- `npm --prefix web run test:e2e -- reader.spec.ts` — not runnable here: the
  container's Chromium headless shell crashes on `about:blank`
  (`Trace/breakpoint trap`), before any app code loads. Environmental,
  unrelated to this change.
- Opt-in Codex/OMLX/pipeline live tests — skipped (no credentials supplied).
- `gofmt`, `go mod tidy -diff`, `git diff --check` — clean.

## 2026-09-02 — Subtitle completeness and reader affordances

- Renamed learner-facing English “shadows” to “subtitles” while retaining the
  existing `shadow_*` persistence and API fields for compatibility.
- Traced a subtitle-free beta paragraph to a corrective Luna response that
  preserved token identities and senses but blanked every `shadow_text` value.
  Translated tokens and constructions now require non-empty subtitle text, and
  corrective prompts explicitly preserve unrelated subtitles.
- Bumped the prompt identity to `reader-analysis-prompt.v5` so the incomplete
  cached paragraph cannot be reused.
- Removed the non-semantic gray dotted underline from ordinary words. The
  yellow wavy marker and shared hover behavior for discontinuous constructions
  remain unchanged.
- Added semantic and browser regressions for subtitle completeness, ordinary
  undecorated words, and retained discontinuous-construction styling.

### Verification

- Focused Go tests for semantics, annotator, reader, and HTTP API — passed.
- Race tests for semantics, annotator, analysis, reader, speech, and HTTP API —
  passed.
- OpenAPI validation/regeneration — passed with no generated diff.
- `npm --prefix web run check` — 0 errors and 0 warnings.
- `npm --prefix web run test:unit -- --run` — 70 tests passed.
- `npm --prefix web run test:e2e -- reader.spec.ts` — 8 tests passed.
- `go test ./... -count=1 -buildvcs=false` and `make verify` — passed.
- Beta-path static production build — passed.
- Authenticated legacy and `gpt-5.6-luna / medium` chunk live tests — passed;
  the Luna response satisfied the stricter subtitle validator.
- `go mod tidy -diff` and `git diff --check` — passed.
- Deployed isolated beta release
  `496062f2c617616401650f66698e055d497b119be936a5089d151c9663d87ae0`.
  Fresh prompt-v5 job `01M1GNKHZ2M97NXCXMWQ80BCDC` completed all three
  paragraphs on attempt 1. The formerly empty “Gelaten…” block now has
  subtitles for all 13 translated tokens; only the proper name is passively
  suppressed. Service and database readiness passed, and production remained
  unchanged.

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
- Deployed isolated beta release
  `290ea09bf11e2a1b78aa1016bdffe6fa76dde1a9b9f9ab68b8e58834674ad20b`.
  A normal prompt-v4 retry of live article `01M1F8FTRA9B20Q27X4J6550V1`
  succeeded on job attempt 1 with all 3 paragraphs, 7 sentences, 98 semantic
  occurrences, and 5 retained turns; service/database readiness remained
  healthy and production was unchanged.

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

## 2026-09-02 — Progressive reader fixes

Implementing plans/progressive-reader-fixes-handoff.md on top of b8cbaf2:

- Migration 007 adds per-block analysis lifecycle and published provenance
  columns, `article_construction_member`, and the seeded owner
  `reader_settings` singleton; deterministic provider-free backfill preserves
  accepted materialization and legacy sentence rows.
- Source sentences are deterministic and server-owned: created with the
  article (`source-sentence.v1`), preserved across reanalysis, lazily created
  for legacy articles, and never rewritten by paragraph publication.
- Contract v3 removes provider-authored sentences; prepared chunks carry
  stable sentence anchors; cache identities include anchors so older rows
  never satisfy v3 work.
- The runner publishes each validated/cache-hit paragraph through
  `PersistAnalysisChunk` (lease- and job-guarded), advances run/job progress
  only after each commit, then runs a non-gating whole-response audit.
  Paragraph failures are durable per block; startup recovery marks interrupted
  blocks failed.
- Exact construction membership (`member_occurrence_ids`) with spans derived
  from maximal adjacent member runs; effective subtitles fall back to the
  sense translation; suppression reasons are `none`, `special_token`, or
  `contiguous_group_member`.
- Narration is queued at article creation before analysis; pronunciations are
  queued per committed paragraph. The narration endpoint no longer waits for
  analysis readiness.
- Reader UI: in-flow interlinear subtitles (no absolute overlay), state-aware
  polling (1s processing / 2s queued / 8s on failure), compact ARIA progress
  surface, per-paragraph lifecycle notes, and exact-member construction runs.
- Owner-wide pronounce-on-hover preference (server singleton + local mirror)
  with optimistic save/rollback, Settings control, and activation-blocked
  hint without disabling the preference.

### Verification recorded during implementation (2026-09-02)

- Focused Go suites (semantics, annotator, analysis, reader, speech, httpapi,
  store, jobs, cmd/doublangu-server) pass with `-count=1`; race tests pass for
  semantics, annotator, analysis, reader, speech, store.
- Migration 007 schema/backfill tests and the deterministic segmenter suite
  pass; reader early-narration, block-scoped publish, superseded-job, and
  display-invariant tests pass; runner progressive publication acceptance
  tests (gated provider, failed reanalysis preservation, cache reuse) pass.
- OpenAPI validates; the generated client is reproducible (identical sha256
  across two generations); svelte-check reports 0 errors / 0 warnings; 76 web
  unit tests pass; 13 reader E2E tests pass (reader.spec, reader-progressive,
  reader-preference), including paragraph-by-paragraph reveal without reload,
  non-intersecting subtitle boxes, ARIA progress advancement, preference
  default/persist/rollback, and the settings-page control.
- `git diff --check` passes. Node v20.19.0 was fetched into /tmp for web
  tooling (the sandbox node v18 cannot run rolldown/svelte-check).

### Code review fixes (2026-09-02)

- Automatic scheduler retries now reinitialize the same job's block lifecycle
  (`ResetBlocksForJob` after the article transitions to processing) so a retry
  republishes earlier published paragraphs from the exact chunk cache instead
  of stopping on a `failed`/`ready` block; a new test claims the automatically
  requeued job without calling `QueueAnalysis` and proves published material
  stays readable and cache-hit paragraph one is not re-provided.
- `PersistAnalysisChunk` materializes `chunk.PriorValidatedSenses` through the
  idempotent `EnsureSenseTx` so a later paragraph referencing an earlier
  paragraph's namespaced sense reuses the durable sense row; regression test
  covers a two-paragraph run referencing `b0:bank-sofa`.
- The runner's final consistency audit now receives the raw chunk responses
  (`MergeChunks` performs its own namespacing); the earlier namespaced form
  could double-prefix local refs (`b0:b0:...`) and fail the audit after
  successful publication.
- `PUT /api/v1/reader/settings` rejects an empty payload (`pronounce_on_hover`
  decodes into a pointer; missing field returns 400 and the preference is
  untouched) with a handler test.
- In-flow subtitle units no longer use `min-width: max-content`, so long group
  units wrap at source spaces within the paragraph; an E2E sweep at 320, 768,
  and 1440 CSS pixels proves no intersecting subtitle boxes and no horizontal
  overflow, including a deliberately long construction subtitle.

Verification: nine focused Go packages pass; race suites pass; svelte-check
0 errors/0 warnings; 76 web unit tests pass; 14 reader E2E tests pass.

### Second code review pass (2026-09-02)

- `MarkAnalysisProcessing` is now job-scoped: the processing transition
  requires `article.analysis_job_id = <job id>`, so a stale job superseded by
  a forced reanalysis can never mark the newer job's article processing and
  wedge every subsequent attempt; regression test covers stale rejection with
  the article left queued and active transition success.
- Migration 007 cancels queued/leased/running v2 analysis jobs
  (`v1.analysis_contract_upgraded`) and moves their articles from
  queued/processing to the recoverable failed state without provider calls, so
  an upgrade can never leave the reader polling a permanently queued article;
  upgrade-fixture test covers both states.
- `ArticleReader` invalidates an in-flight settings load once a save begins
  (version counter), so a stale initial GET can never revert a newer
  successful toggle; E2E test delays the initial GET past a completed PUT and
  asserts the disabled value survives.

Verification: nine focused Go packages pass, race suites pass, svelte-check
0/0, 76 web unit tests pass, 15 reader E2E tests pass.

### Beta deployment (2026-09-02)

- Merged the reviewed implementation to `main` at `8b85a3a` and deployed
  immutable beta release
  `f8a864c3c59ce5986e1cbd50f28cb665c1ea12b433b18003c3bdb7f520610ef6`.
- The isolated `doublangu-beta` service restarted successfully. Database
  readiness, pinned Codex authentication, TLS, the unauthenticated Basic Auth
  challenge, authenticated shell delivery, and authenticated proxied health
  all passed. Existing protected credentials were retained and production was
  unchanged.

## 2026-09-02 — Configurable analysis provider pipeline (implementation start)

Implementing plans/configurable-analysis-provider-pipeline-handoff.md on the
plan's reviewed baseline 1aeb9a7 (clean main, migration 007 latest, contract
reader.analysis.v3 / prompt v6 intact; no reconciliation needed).

- Checkpoint 1 (complete): new acyclic `internal/pipeline` package (stage
  registry, linguistic/translation contract and prompt identities v1,
  BindingSnapshot/ProfileSnapshot, domain-separated options/snapshot hashes);
  `internal/semantics/stages.go` with strict linguistic/translation artifact
  decoders, the linguistic validator mirroring v3 source-side invariants and
  sorting constructions with server-assigned b<c> IDs, the translation
  correspondence/source-copy validator, and the exact-id merge that still
  passes unchanged v3 chunk validation; `internal/annotator/stages.go` with
  source-side and translation-only prompts, closed schemas, and correction
  builder. Verified without any provider.
- Checkpoint 2 (complete): `internal/config/providers.go` strict file
  configuration (id/label rules, URL allowlists incl. loopback and
  100.64/10 Tailscale/CGNAT, api_key_env pattern, file ownership/mode rule,
  provider fingerprints over sanitized identity, per-type option codecs);
  annotator Provider/Session/ResolvedBinding boundary, Registry, Codex
  app-server session reusing protocol primitives, bounded OpenAI-compatible
  client (catalog, history-preserving corrections, usage/timing, redaction,
  classified failures), and the shared two-correction stage executor with
  typed provider/stage_validation/final_validation phases. Fake process/HTTP
  tests cover both transports through the executor. main.go wiring of the
  registry is deliberately sequenced with CP4/CP5 so the legacy runner keeps
  working until the pipeline payload cutover.
- Checkpoint 3 (in progress): migration 008 (profiles/bindings/settings,
  stage cache, stage attempts/turns, run/article/block/semantic-sense
  provenance columns, deterministic cancellation of old-format jobs with
  v1.analysis_pipeline_upgraded) with fresh/upgrade/cascade tests passing;
  legacy settings/caches and accepted materializations survive 008.

Verification so far: 11 focused Go packages pass; gofmt/tidy/diff clean;
race suites green for pipeline/semantics/annotator/config.

### Pipeline checkpoint 3 (partial) and readiness matrix (2026-09-02)

- Migration 008 tests pass (fresh schema, profile cascade/RESTRICT, 007->008
  upgrade preserving legacy settings/caches/materializations, old-format job
  cancellation with v1.analysis_pipeline_upgraded).
- `internal/analysis/profiles.go`: transactional ProfileStore (list/get/
  create/replace/rename/delete/activate, NOCASE-unique names, strict binding
  shape and per-binding provider-type option codec validation, seed-only-
  when-empty bootstrap with automatic activation). Unit tests cover CRUD,
  activation, active-delete rejection, partial/unknown/bad-type rejections,
  seeding, and canonical binding normalization.
- Readiness matrix run: 11 focused Go packages pass; race suites pass for
  pipeline, semantics, annotator, config, analysis; svelte-check 0/0; 76 web
  unit tests pass; 8 reader E2E tests pass; gofmt/tidy/git diff clean.
- Remaining plan work (sequenced): CP3 queue snapshot semantics, history
  attempts/turns and recovery wiring; CP4 runner/cache/provenance; CP5 owner
  API and generated client; CP6 settings/run-detail/reader controls; CP7
  documentation and full matrix incl. owner-authorized live OMLX/Codex tests.
  The worktree is intentionally uncommitted; no deploy or live provider test
  was performed.

### Pipeline checkpoint 3 follow-up and readiness rerun (2026-09-02)

- `internal/analysis/stage_history.go`: durable per-stage attempts and turns
  (start/finish/append/list) against the migration-008 tables with hard
  retention bounds (1 MiB prompt/response reject, 64 KiB metadata and 16 KiB
  excerpt truncation with explicit per-field flags), plus
  `RecoverInterruptedStageAttempts` (running attempts become failed with
  v1.analysis_interrupted, preserving artifacts and recorded duration),
  wired into server startup next to the existing article recovery.
- Migration 008 turn table adjusted (provider_stderr_excerpt and
  per-field truncation flags) since it has not been deployed anywhere.
- Stage-history tests pass (attempt uniqueness, turn recording, oversized
  rejection, interrupted recovery, ordered listing).

Readiness rerun: analysis/store/server suites pass; gofmt/git diff clean;
24 changed files, uncommitted. Remaining plan work unchanged: CP3 queue
snapshot semantics; CP4 runner/stage cache/provenance cutover; CP5 owner API
and generated client; CP6 settings/run-detail/reader controls; CP7 full
documentation and the owner-authorized live matrix.

### Pipeline CP3/4 partial additions (2026-09-02)

- `internal/pipeline/jobpayload.go`: canonical immutable JobPayload with
  strict decode/encode and full verification (analysis contract, pipeline
  version, registered profile bindings, recomputed snapshot hash, fresh
  mode); round-trip and tamper tests pass.
- Reader: `CreateArticleQueuedWithProfile` persists the resolved profile
  snapshot columns (id/name/JSON/hash) on the article; legacy
  `CreateArticleQueued` leaves them blank. Persistence test covers both.
- Remaining: reader QueueAnalysis snapshot/retry semantics, runner two-stage
  flow with stage cache and provenance, owner API/UI checkpoints, live tests.

### Pipeline checkpoint 4 (core) (2026-09-02)

- `internal/analysis/stage_cache.go`: exact stage cache identities (canonical
  paragraph input hashes over sentences/tokens/candidates/prior-stripped
  senses, upstream linguistic artifact hash for translation, artifact hashes)
  with read/save helpers; cached artifacts are stored in their raw
  provider shape so strict decoders and validators revalidate every hit.
- `internal/analysis/pipeline_runner.go`: PipelineRunner claims pipeline
  jobs, strictly decodes and verifies the JobPayload, fails closed when any
  configured provider fingerprint changed, and runs the two-stage per-
  paragraph loop: per-stage attempts (cache hit/miss/bypassed) and executor
  turns recorded through the stage history store, linguistic then translation
  execution or cache revalidation, exact-id cache writes after local
  validation, namespace/revalidate, publication through
  PersistAnalysisChunkWithProvenance (block profile columns and sense
  translation provenance), progress only after commit, and the non-gating
  final audit. Failure paths record the failed stage/provider on the run.
- Gated end-to-end test proves the acceptance case: paragraph zero's
  linguistic stage succeeds while its translation stage is blocked, nothing
  is published, and the paragraph appears only after the gate releases; final
  state records 4 stage attempts, 4 turns, and 4 exact cache rows for two
  paragraphs.

Readiness: analysis/reader/pipeline/semantics/annotator/store suites pass;
gofmt and diff clean. Remaining plan work: CP5 owner API/OpenAPI, CP6 UI,
CP7 full matrix and docs.

### Pipeline checkpoint 4/5 progress (2026-09-02)

- PipelineRunner end-to-end acceptance verified with a gated fake provider:
  paragraphs publish only after both stages complete; stage attempts, turns,
  and exact stage cache rows are recorded; the run finishes ready with empty
  failed_stage_id.
- Owner API (interim paths under /api/v1/analysis): GET providers (sanitized
  descriptors, per-provider live catalogs, health), GET/POST profiles,
  GET/PUT/DELETE profile by id, GET/PUT pipeline-settings (active profile),
  and POST providers/{id}/test running the fixed "De kat zit op de mat."
  fixture through the real stage executor with only status/duration/stable
  code returned. CSRF, no-store, active-delete conflict, empty-body 400, and
  unknown-provider 404 are covered by handler tests.
- main.go loads DOUBLANGU_PROVIDER_CONFIG into the annotator registry and
  seeds the bootstrap profile when none exist (logging only provider ids and
  stable codes on failure); routes registered behind the existing owner auth.
- All 11 focused Go packages pass; gofmt/diff clean.

Remaining plan work: CP5 OpenAPI/generated client for the new routes and
article/run pipeline provenance, articles reanalyze profile override wiring,
legacy settings/models migration of in-repo callers; CP6 Settings/run-detail
reader controls UI; CP7 full matrix and docs.

### Pipeline CP5 owner HTTP wiring (2026-09-02)

- ArticleHandler.ConfigurePipeline attaches the profile store and provider
  registry; main.go activates pipeline behavior only when a provider config
  file exists.
- POST /api/v1/articles creates through the active profile (queues a pipeline
  job with the immutable snapshot) or, when no usable profile is active,
  stores a readable article with analysis failed and the first block failed
  with v1.analysis_profile_unavailable (no invalid job is queued).
- POST /api/v1/articles/{id}/reanalyze is strict: profile_id is rejected
  unless fresh:true; fresh resolves the named or active profile, non-fresh
  reuses the stored snapshot, legacy articles adopt the active profile once;
  no usable profile yields 503 v1.analysis_profile_unavailable.
- Article payloads now expose analysis_pipeline {profile_id, profile_name,
  snapshot_hash} loaded from the article row; reader gains
  HasPipelineSnapshot and CreateArticlePipelineUnavailable (first block
  failed, no job).
- Handler tests cover creation with active profile, unavailable fallback,
  non-fresh profile rejection, unknown profile 404, fresh override switching
  stored snapshots, fresh-active resolution, and stored-snapshot retries.
  httpapi/analysis/reader/main suites green; gofmt clean.

### Pipeline CP5 OpenAPI/client + run provenance (2026-09-02)

- contracts/openapi.yaml gains the provider list/test, profile CRUD, and
  pipeline-settings operations with strict schemas (CP12 400 matrix
  satisfied); reanalyze documents profile_id valid only with fresh:true;
  Article and run payloads expose analysis_pipeline provenance.
- Generated client regenerated twice and proven byte-identical; CP12
  validation and svelte-check pass.
- analysis_run list/detail now surface profile_id/profile_name/
  profile_snapshot_hash for pipeline runs (omitted when blank for legacy
  runs).
- Interim deviation: legacy /analysis/models and /analysis/settings endpoints
  remain registered alongside the new pipeline endpoints until the CP6 UI
  moves; pipeline-settings is temporarily mounted at
  /api/v1/analysis/pipeline-settings and will be folded into /settings after
  the legacy UI cutover.
- All 11 focused Go suites green; gofmt and diff clean; 42 changed files.

### Pipeline CP6/CP7 UI, docs, and verification (2026-09-02)

- web/src/lib/settings (new, with AGENTS.md): analysisProfiles helpers +
  tests and the AnalysisPipelinePanel (provider cards with live safe
  conformance tests, profile create/edit/delete, active-profile radio, omlx
  numeric option fields, codex effort select; endpoints and secrets never
  rendered). Settings page mounts it under the legacy section; servers without
  a provider config show an explanatory muted note.
- Run detail shows pipeline provenance rows and per-stage attempt lists
  (status/provider/model/cache disposition/duration/stable code); the Go run
  payload now includes stage_attempts (contract-covered, client regenerated
  twice byte-identical).
- Reader: pipeline articles show the stored profile badge; failed articles
  get "Retry with saved profile"; fresh runs carry an explicit profile picker
  (defaulting to the stored or active profile) and send profile_id only with
  fresh:true. Legacy model/effort controls unchanged for non-pipeline runs.
- client.ts wrappers for providers/profile/settings/test endpoints; client
  tests cover routing incl. the fresh/profile_id body rule.
- Docs: README pipeline section, ARCHITECTURE 008 section, and
  config/provider.example.json (placeholder model ids/env-name secret only).
- Full matrix pass: 11 focused Go suites, race subset, go test ./..., go mod
  tidy -diff, gofmt, git diff --check, CP12 validation, byte-identical API
  regeneration, svelte-check 0/0, vitest 83, Playwright reader 8 — all green.
- Rollback note: 008 is additive and compatibility mode returns by removing
  the config file; nothing was committed or deployed.
### Owner decisions (2026-09-02)

- Opt-in live Codex/OMLX/mixed tests: skipped and reported as owner-gated
  (no live providers invoked; no provider credentials used).
- Deployment: declined — no commit, no push, no beta or production change.
  Everything remains uncommitted on the working tree for owner review.

### P1 review fixes: worker dispatch, lease liveness, superseded failure guard, correction context (2026-09-03)

1. Production worker: `main.go` loads DOUBLANGU_PROVIDER_CONFIG before any
   worker starts; a configured-file load error aborts startup (exit 1) instead
   of silently enabling legacy mode. Pipeline mode starts the two-stage
   `analysis.NewPipelineRunner` and never the legacy runner, so legacy code
   cannot claim pipeline payloads; compatibility mode keeps the legacy runner.
2. Lease liveness: PipelineRunner renews the job lease on a 20 s heartbeat
   ticker for the whole run (interval overridable in tests) and verifies the
   lease before executor entry and before every cache/history/publication
   write; lease-lost/expired stage errors route to `v1.analysis_lease_lost`
   without touching article state or newer-run history.
3. Superseded failure guard: new job-scoped `reader.MarkAnalysisFailedForJob`
   transitions the article only when `analysis_job_id` still matches; the
   pipeline runner's failure path uses it and logs (never overwrites) when a
   newer job owns the article.
4. Correction context: the stage executor preserves the rejected raw artifact
   into the corrective prompt, and `openAICompatibleSession.Turn` retains the
   message history across repeated Turn calls (schema still sent per request),
   so OMLX corrections see their own prior instructions and rejected output.

New tests: executor corrective prompt embeds the rejected artifact; OMLX
Turn-to-Turn history; reader supersede conflict; pipeline heartbeat keeps a
lease live during a blocked provider call; lease-loss runs skip article
failure and publication. All 11 focused Go suites, the race subset for
annotator/analysis/reader, gofmt, tidy, and diff checks pass.

### Review findings batch 2: usable bindings, failure turns, provider code, run-detail diagnostics, heartbeat race (2026-09-03)

1. Profile saves (POST/PUT) now validate bindings against live enabled
   provider instances: unknown or disabled providers and type mismatches are
   rejected, and model bindings that changed from the stored profile must
   appear in a successful catalog listing; unchanged bindings skip the
   catalog so renames/re-options work while a catalog is briefly down.
   Catalog outages surface 503 v1.analysis_profile_unavailable; bad bindings
   400 v1.validation_error.
2. Failed stage turns are persisted: runLinguistic hands executor turns back
   on error and the linguistic failure branch records them; runTranslation
   records executor turns before returning failures. Provider errors and
   rejected corrective outputs now reach analysis_stage_turn.
3. Provider-phase detection inspects the typed StageError.Phase ("provider")
   instead of the stage-qualified string, so offline/auth/timeout failures
   surface as v1.analysis_provider_unavailable.
4. Run detail returns full pipeline attempts (provider type/fingerprint,
   contract/prompt versions, input/upstream/options hashes, requested and
   reported models, request id, finish reason, usage/timing/metadata JSON,
   stderr, errors) with nested stage turns from analysis_stage_turn; the UI
   renders them and no longer claims cache-only results for pipeline runs.
5. Heartbeat progress is stored in sync/atomic.Int64 and loaded by the
   heartbeat goroutine (no shared *int race, no progress regression).

Coverage: httpapi rejection tests (unknown/disabled provider, unlisted model,
catalog outage 503, unchanged-model rename skips catalog, changed model
requires it); analysis end-to-end failure run asserting
v1.analysis_provider_unavailable with the failed turn retained in run detail;
OpenAPI stage attempt/turn schemas regenerated byte-identical; all 11 Go
suites, race subset, svelte-check 0/0, and vitest 83 pass; gofmt/tidy/diff
clean.

## 2026-09-02 — Review fixes: pipeline cancellation, profile validation, provenance, API DTOs

Applied the five material findings from the review of the configurable analysis
provider pipeline against the working tree.

- Cancellation/lease loss now propagates: the runner derives a per-run
  cancelable context; the heartbeat cancels it on owner cancellation or lease
  loss and retains the first error; provider calls abort immediately instead of
  occupying the sole worker until timeout. Lease ownership is re-verified
  before every success/failure artifact write (turns, attempt finish, cache,
  paragraph publish); a reclaimed worker records no turns or failure state for
  the in-flight stage and touches no article state. Covered by
  TestPipelineRunnerCancelAbortsBlockedProviderCall.
- Stored profiles are revalidated before activation (409 on disabled/removed
  providers) and before article resolution and fresh-run selection (503),
  through one shared usableProfileBindings check; covered by
  TestPipelineArticleReanalyzeRejectsDisabledProvider.
- Profile save validates the full provider/model/options tuple: options are
  canonicalized first, options-only changes trigger the catalog check, and
  Codex bindings verify the reasoning effort against the model's advertised
  efforts via annotator.SupportsSelection.
- Stage attempts now persist binding options/options hash/requested model at
  start; both terminal paths thread the executor StageAttemptResult (reported
  model, request id, finish reason, usage/timing/metadata) and compute the
  duration; cache hits record cache_disposition=hit plus source_cache_id with
  no turns. Covered by
  TestPipelineRunnerPersistsStageProvenanceAndCacheIdentity.
- Provider and profile responses use explicit DTOs matching the OpenAPI
  schemas (additionalProperties:false): endpoint_label, config_fingerprint,
  request_timeout_ms, and snapshot-only binding fields no longer serialize;
  wire-shape assertions added to TestPipelineAnalysisProfilesAndSettings.

Verification: gofmt clean, go mod tidy -diff clean, focused suites
(config/semantics/annotator/analysis/reader/httpapi/store/cmd) pass, race
suites for analysis and httpapi pass, openapi validate + generation
reproducible, svelte-check 0/0, vitest 83 pass. Known follow-up:
HistoryStore.GetRun exposes options_hash but not the raw options JSON of each
stage attempt; the remaining provenance columns are already selected and
rendered.

## 2026-09-02 — Review round 2: preflight failure, catalog service, efforts, run options, usage totals

- Terminal preflight failures (provider disappeared, fingerprint/type changed,
  source/prepare mismatch) now transition the owning article and its first
  unresolved paragraph to the failed state with the same stable code once job
  retries are exhausted; a terminally failed job can no longer strand an
  article in queued. Covered by TestPipelineRunnerPreflightTerminalFailureFailsArticle.
- Added the shared per-provider catalog service (five-minute TTL, last-good
  retention, stale flag, sanitized refresh error) in httpapi/provider_catalog.go.
  ServeProviders honors refresh=true&provider_id for exactly one provider and
  never drops models on a transient failure; profile create/update, settings
  activation, active-profile resolution, and fresh-run profile selection all
  validate the full model/options tuple against the non-stale catalog, so a
  config change that removes a model or supported effort cannot be activated
  or queued. Covered by TestPipelineProviderListingRefreshAndStale and
  TestPipelineActivationRequiresCurrentModelAndEffort.
- Model capabilities now flow to the browser: AnalysisProviderModel carries
  supported_reasoning_efforts, the settings editor renders only the selected
  model's advertised efforts, and the wire payload no longer coerces an
  unknown effort to low (server validation is authoritative). Frontend unit
  tests updated for preservation instead of coercion.
- Run detail now includes each stage attempt's stored options object
  (StageAttemptSummary.Options via GetRun, options_hash retained), declared in
  the OpenAPI AnalysisStageAttempt schema and rendered on the owner run page.
- Stage executor accumulates usage and timing totals across initial and
  corrective completions instead of overwriting them with the final request;
  per-turn completion metadata remains on the turn records. Covered by
  TestExecuteStageAggregatesUsageAcrossCorrections.

Verification: gofmt/tidy/diff clean; focused suites (config/semantics/
annotator/analysis/reader/httpapi/store/cmd) pass; race suites for annotator,
analysis, and httpapi pass; go test ./... passes; openapi validate + generation
reproducible; svelte-check 0/0; vitest 15 files / 85 tests pass. Known
follow-up: attempt-level request id/finish reason/metadata still reflect the
final completion (usage/timing are aggregated; per-request ids are not stored
per turn).

## 2026-09-02 — Final readiness checks for review round 2

- `make verify` (vet, check-no-network, manifest/auth foundation, login UI
  compile + vitest, fingerprint integration) passes end to end.
- Reader E2E `npm --prefix web run test:e2e -- reader.spec.ts` passes 8/8
  (Playwright needs the Go toolchain on PATH for the spawned server).
- Broad `go test ./...` passes; the earlier `go build ./...` complaint is
  limited to plugins/official/sample, a plugin main package that only builds
  under `-buildmode=plugin` (go vet and go test on it pass inside make verify)
  and is untouched by this work.
- Final tree review: only intended files changed/added by rounds 1-2;
  gofmt/diff checks clean; pre-existing untracked artifacts
  (`doublangu-server` binary, example provider config) preserved.

## 2026-09-03 — Review findings F4/F5/F6 settings cutover + tests

- Settings page cut over to the pipeline panel unconditionally: the legacy
  Article analysis editor, `/analysis/models` + legacy `/analysis/settings`
  calls, and `/analysis/pipeline-settings` are gone. `GET /analysis/settings`
  now returns the pipeline selection shape (`active_profile_id`) and the
  compat-mode empty state explains that analysis is unavailable until a
  provider is configured. Run history renders legacy model/effort provenance
  or profile + per-stage bindings.
- Profile creation never auto-activates: `saveProfile` persists without
  touching settings, and the per-profile radio (`chooseActive`) is the only
  activation path, issuing an explicit `PUT /analysis/settings`.
- Profile names validate on client (`profileNameError`) and server with the
  same rules (2-64 chars, letters/digits/space/`-_().+`, single spaces);
  blank/oversized/illegal names are rejected, never coerced.

Verification: `go test ./...` passes (GOCACHE redirected to /tmp — the
default cache path is read-only in this container); `make verify` passes end
to end with the Go toolchain on PATH; `npm --prefix web run check` 0 errors /
0 warnings; vitest 16 files / 98 tests pass, including rewritten
`settings-page.test.ts` (compat vs pipeline modes, no legacy calls) and new
`AnalysisPipelinePanel.test.ts` (create-then-activate without implicit PUT);
`generate:api` output hash-stable; `git diff --check` clean. Reader E2E and
live provider tests remain unrunnable here (no container Chromium / creds).

## 2026-09-03 — Latest-review 10-finding verification pass

- Re-verified every finding in `plans/reviews/latest_review.md` against the
  current tree instead of trusting prior-turn summaries: F1 persisted import
  (`TestCompatibilityProviderFileImportsPersistedSettings`), F2 key redaction
  (unit + `TestPipelineProvidersRedactEchoedKey`), F3 URL stripping
  (`TestOpenAITransportErrorsHideEndpointURL`), F4 cutover (routes, OpenAPI,
  client, page), F5 explicit activation (component test), F6 name alignment
  (OpenAPI 80 / input maxlength 80 / `nameBlocked` in `canSave`; the remaining
  `maxLength: 120` is the unrelated worker-enrollment schema), F7 detached
  refresh context, F8 commit-under-mutex, F9 envelope reported model, F10
  provider-owned transport — each with its prescribed test present and green.
- Only change made: replaced 7 stale `/api/v1/analysis/pipeline-settings`
  request-URL strings in `internal/httpapi/pipeline_analysis_test.go` with
  `/api/v1/analysis/settings` (handler invoked directly; cosmetic).

Verification: gofmt clean; `go test ./... -count=1 -buildvcs=false` all pass;
catalog race tests pass; `make verify` passes; `validate:openapi` passes;
`npm --prefix web run check` 0/0; vitest 16 files / 98 tests pass;
`git diff --check` clean. Reader E2E and live provider tests unrunnable here.

## 2026-09-03 — Latest-review 10-finding implementation pass (commit 2cda0e2)

Fixed all ten findings in `plans/reviews/latest_review.md`:

- F1 (P1 cache identity): `StageCacheSpec` carries provider type + config
  fingerprint end to end (spec, INSERT/SELECT/conflict, runner bindings);
  migration 009 recreates the unique index with the extra columns so
  pre-migration empty-fingerprint rows naturally miss. Tests: fingerprint-only
  change misses both stages, type change misses, legacy rows miss.
- F2 (P1 translation hash): split `ChunkInputHash` (linguistic, source-only
  priors) from `TranslationChunkInputHash` (exact full prior senses) with a
  stage-domain separator; runner uses each for its stage. Test: English-only
  prior edit keeps the linguistic hash, changes the translation hash.
- F3 (P1 error bodies): `classifyHTTPStatus` emits allowlisted
  status/classification text only; response bodies never enter errors.
  Removed `redactAPIKey`/`sanitizeExcerpt`. Tests: 4xx/5xx hostile bodies
  absent from catalog + turn errors, API `last_error`, and stored history.
- F4 (P2 size limits): executor materializes the schema once per turn and
  rejects oversized prompt/schema (1 MiB) before `session.Turn` as a local
  input error; `AppendStageTurn` enforces the schema bound as a storage
  invariant. Tests: zero provider turns for both fixtures, retention rejects.
- F5 (P2 Codex timeout): dropped the constructor-wide timeout; Codex and
  OMLX instances derive from `ResolveTimeoutSeconds(entry)` (default 600s
  preserves old behavior). Test: 30s entry observed on instance + descriptor.
- F6 (P2 leader cancellation): refresh runs as an async service operation
  bounded by the advertised provider timeout; every waiter including the
  creator selects on its own context. Test: canceled leader returns promptly,
  joiner still receives the shared single-call result (race-clean).
- F7 (P2 fresh profile): reader lists only usable profiles, defaults fresh
  to the usable active profile (never the stored snapshot), discloses the
  selector behind "Fresh analysis…", and shows a Settings link instead of Run
  when none usable or loading failed. Component tests for all four cases.
- F8 (P2 provider card): `endpoint_label` + `retrieved_at` added to the DTO,
  OpenAPI, generated client, and provider card; URL/fingerprint/timeout/
  secret stay excluded. API test asserts presence + absence.
- F9 (P2 run detail): `GetRun` returns the decoded `profile_snapshot` plus
  `failed_stage_id`/`failed_provider_id`; OpenAPI gains the snapshot schema;
  detail page renders bindings + failed binding. Round-trip test included.
- F10 (P2 truncation flags): migration 010 adds five attempt flag columns;
  usage/timing/metadata bound as valid-JSON sentinels (64 KiB), stderr/detail
  as excerpts (16 KiB); attempt + turn flags flow through GetRun/OpenAPI and
  render as "(truncated)" markers. Boundary + round-trip tests included.
- Incidental: migration pins in store tests updated 8 → 10 with 009/010
  schema assertions; stale `pipeline-settings` test URLs renamed; a providers
  test now asserts the intended `endpoint_label` presence.

Verification: `go test ./... -count=1 -buildvcs=false` all pass; race suite
(annotator/analysis/httpapi) passes; `go mod tidy -diff` clean; gofmt clean;
`validate:openapi` passes; API generation byte-stable across runs;
`npm --prefix web run check` 0/0; vitest 17 files / 101 tests pass;
`make verify` passes; reader E2E 8/8 passes; `git diff --check` clean.
Opt-in live Codex/OMLX/pipeline tests not run (no credentials/endpoints).

## 2026-09-03 — Follow-up review: 3 remaining findings fixed

Against base `2cda0e2`, fixing the three defects left from the prior pass:

- F1 (pre-session size check): `executeStage` materializes and validates the
  initial prompt/schema via `checkInvocationSize` before `OpenSession`, so an
  oversized input never launches a provider process; corrective turns keep
  the in-loop check. The stub provider counts opens; the regression test now
  asserts opens and turns both stay zero.
- F2 (on-demand profiles): the reader no longer fetches profiles on article
  open. `openFreshOptions` loads on first workflow open; an article counts as
  loaded only after success, failures keep an explicit Retry button, and
  navigation resets without fetching. Tests assert zero profile requests
  before opening and a second request on retry after a transient 500.
- F3 (explicit usable profile): one fresh-run workflow serves pipeline and
  migrated legacy articles; the empty fallback option is gone and Run stays
  disabled without a selected usable ID, so every fresh request carries
  `{fresh:true, profile_id}`. Tests cover invalid-active fallback, deliberate
  selection changes, and legacy articles.

Verification: `go test ./...` passes; prescribed race suite (semantics,
annotator, analysis, reader, httpapi) passes; gofmt/tidy/vet/diff clean;
`validate:openapi` passes; API generation byte-stable; `check` 0/0; vitest
17 files / 103 tests pass; reader E2E 8/8; `make verify` passes. Live
provider tests not run (no credentials/endpoints).
## 2026-09-03 — Mac LLM relay: speech worker daemon (checkpoints 1–3 + CP4 build)

Implemented the Mac side of `plans/in-progress/mac-llm-relay-speech-worker-handoff-reviewed.md`
(review baseline `main` @ `2cda0e2`), mirroring the authoritative backend wire contract. Backend
work and the physical beta pairing are explicitly out of scope for this pass.

- **Config + migration (CP1):** `RelayConfig` (enabled/base_url/request_timeout_seconds, defaults
  off / `http://127.0.0.1:8899/v1` / 540 s, timeout 30–540) added as the only newly optional top-level
  config key; every old key stays required, unknown keys stay rejected. A v0.1 file without `relay`
  decodes, validates, and is atomically rewritten once with the default block through
  `AppPaths.writePrivate` (`SpeechWorkerConfiguration.loadFromDisk`). URL rules enforced: absolute,
  no user/pass/query/fragment, path exactly `/v1`, https any host, http only literal loopback
  (`127.0.0.0/8` or `::1`; `localhost` rejected).
- **Keychain (CP1):** `relay-api-key` account added; the key never enters config.json, logs, job
  payloads, or server requests other than the local provider Authorization header. Settings
  SecureField starts empty and is never read back; UI shows only "stored"/"no key stored".
- **RelayHTTPClient (CP1):** separate ephemeral `URLSession` (request/resource timeout from config,
  `waitsForConnectivity=false`), redirects rejected via session delegate, bounded reads at 2 MiB+1
  byte, typed local errors (`cannotConnect`/`connectionLost`/`timedOut`/`http`/`modelUnknown`/
  `oversized`/`invalidResponse`/`canceled`), narrow unknown-model recognition (structured
  `error.code`, or 400/404 message naming the exact model + "not found"/"unknown model"), and
  bounded API-key-redacted excerpts for local diagnostics only.
- **Protocol (CP2):** strict relay wire models (capability, chat/models payloads, results, usage,
  known-OMXL-keys-only timing, recursive JSON value for schema pass-through). `LeaseResponse` is
  job-type discriminated: TTS branch validates exactly as before and forbids relay keys; relay
  branch requires `operation`+`relay`, tolerates zeroed speech fields, validates the payload, and
  fails on unknown job types. `LeaseRequest` takes a single `LeaseLane` (mixed lanes
  unrepresentable); `EnrollRequest` carries optional `llm_relay_capabilities` and v0.2 enrolls relay
  support regardless of the enabled toggle; `WorkerInfo` tolerates the new optional relay fields;
  `CompletionMetadata.artifact` is optional; `WorkerClient` exposes only `completeSpeech`
  (metadata+audio) and `completeRelay` (metadata+result, 2 MiB bound enforced before I/O). Lease
  decode bound raised to 2 MiB+slack for schema pass-through.
- **RelayLoop + lifecycle (CP3):** serial relay lane with relay-only lease requests, 1 s idle delay,
  1→300 s offline backoff, 30 s heartbeats during jobs, per-job task so server cancellation/stale
  409 cancels the in-flight local URLSession work, `v1.relay_canceled` best-effort ack, and the
  §5.8 code/retry matrix (cannot-connect/5xx retry=true; timeout/auth/malformed/model-unknown/
  canceled retry=false). 401/403 stop the lane as failed; 400/protocol mismatch stop the lane as
  `requiresReenrollment`. No journal, spool, or local result persistence. `AppState` starts/stops
  the lanes independently (speech setup gaps never block relay and vice versa), and
  `saveRelayConfiguration`/`clearRelayAPIKey`/`testRelayConnection` restart only the relay lane.
  Clearing the key disables relay and deletes the Keychain item.
- **UI:** Settings gains a Relay tab (status, enabled toggle, base URL, timeout, key SecureField,
  stored-key indicator, Save/Clear key/Test connection, bounded model-list result); the menu bar
  shows separate Speech and Relay status rows.
- **CP4 code side:** `macos/speech-worker/VERSION` and `WorkerConstants.appVersion` → 0.2.0;
  `./build-app.sh --development` builds and ad-hoc-signs
  `dist/macos/Doublangu Speech Worker.app` (Info.plist 0.2.0, codesign "satisfies its Designated
  Requirement").

Verification (from `macos/speech-worker`): `xcrun swift-format lint --recursive app/Sources
app/Tests` clean; `swift test --package-path app --parallel` exit 0 (88 tests: config/migration/URL
matrix, protocol negative matrix incl. duplicate/unknown keys and multipart shapes, relay HTTP
client against a real loopback NWListener server incl. redirect rejection/oversized reads/local
timeout/cancellation/key redaction, RelayLoop stub-client behaviors incl. heartbeat survival,
server cancellation, stale-409 without ack, retry-flag matrix, parallel TTS observation, and
AppState save/clear/misconfigure flows); `swift build --package-path app -c release` exit 0;
`./build-app.sh --development` exit 0; repo `git diff --check` clean. Live proof on this Mac with
OMLX 0.6.4 at `127.0.0.1:8899` (`DOUBLANGU_TEST_RELAY_LIVE=1`): `testLocalOMLXListsPinnedModels`
returned both pinned models and `testLocalOMLXChatCompletionParity` completed a real
`chat_completion` (json_schema response format accepted, content ≤1 MiB, finish_reason stop).
No relay prompt/result is ever written to disk by construction (no journal/spool paths in the
relay lane; asserted by test).

Remaining owner steps (CP4, not runnable from this checkout): deploy backend checkpoints 1–2 to
beta, install the v0.2.0 app on `dev-ren-mac`, perform the single Replace Enrollment, save relay
config + key on that Mac, confirm the beta provider catalog lists both models, run the real beta
article with parallel TTS observation, and the OMLX-offline/explicit-retry proof; rollback per plan
§13 (disable relay → clear key → reinstall v0.1 if needed).

## 2026-09-03 — Review findings: relay run intent, canceled mapping, fixed wrapper, completion shapes

- **P1 (Mac):** `AppState` gained an explicit run intent (`workerRunning`, published). `start()`
  sets it before evaluating lane prerequisites; `stop()` clears it and tears both lanes down.
  `restartRelayLaneIfNeeded()` now restarts the relay lane only while the intent is set, so
  start → stop → save-enabled-relay-config no longer resurrects relay leasing; while stopped,
  config mutations only refresh `relayStatus`. The menu's Start/Stop button is driven by the run
  intent (with Open Setup still offered when speech setup/enrollment is missing), so a
  relay-only worker whose speech setup is unavailable can always be stopped from the menu.
  Regression tests use an enrolled config + full Keychain identity with a lease-counting client
  override (`AppState.relayClientOverride`, test-only): `testStopPreventsRelayRestartOnRelayConfigSave`
  and `testRelayOnlyRunningWorkerCanBeStoppedWhileSpeechSetupRequired`.
- **P2 (Go, `internal/llmrelay`):** `mapWaitError` keeps its parent-context checks first and now
  maps a terminal `v1.relay_canceled` with a live parent context to `annotator.CodeUnavailable`
  instead of falling through to the generic provider failure. `TestAdapterChatCompletionMapping`
  covers all five worker relay codes (auth, invalid response, unreachable, model unknown,
  canceled).
- **P2 (Mac):** `RelayJSONSchema.validate()` now enforces the fixed wrapper
  (`name == "doublangu_stage_artifact"`, `strict == true`, schema object), matching the Go
  validator; `testInvalidResponseFormatWrapperIsRejected` gained wrong-name and strict=false
  cases, and the large-schema round-trip test uses the real artifact name.
- **P3 (Go, `internal/httpapi`):** `ServeComplete` requires only `metadata` at the HTTP layer and
  always hands the observed `audio`/`result` parts to `workers.Service.Complete`, which judges the
  shape after loading the lease (`ErrUploadRejected` for TTS, `ErrRelayRejected` for relay). New
  `TestServeCompleteMissingPayloadJudgedByJobType` covers relay+metadata-only,
  relay+audio, TTS+metadata-only, and TTS+result, asserting the job-specific 422 codes.

Verification: `go test ./internal/llmrelay/ ./internal/httpapi/ -race -count=1` pass; gofmt clean;
`xcrun swift-format lint` clean; `swift test --package-path app --parallel` exit 0;
`swift build -c release` exit 0; `build-app.sh --development` rebuilt the v0.2.0 bundle;
`git diff --check` clean.

## 2026-09-04 — Configurable worker server URL; rename to "Doublangu worker"

The mac worker hardcoded the beta service URL (`WorkerConstants.baseURL`) and
`validate()` pinned the host+path, so the deployment target was baked into the
binary and the public source. It is now user configuration:

- `SpeechWorkerConfiguration.baseURL` is `URL?` (`base_url` optional in the strict
  decode; fresh default configs omit it). Validation accepts any HTTPS target plus
  plain HTTP for literal loopback hosts only — mirroring `RelayConfig` rules. No
  default URL ships in source; a fresh install reports the new
  `AppStatus.serverURLRequired` ("Server URL required") until it is set.
- Settings → Worker gained a Server section (`saveServerURL` in `AppState`):
  validates the URL, persists the config, and rebuilds both lanes since both the
  lease and relay loops reach the Doublangu server through `WorkerClient`.
  Clearing the URL returns the worker to `serverURLRequired`; the relay lane
  reports `misconfigured` while enabled without a server URL.
- `WorkerClient.init` lost its default `baseURL` argument; the server live test
  now takes `DOUBLANGU_TEST_SERVER_BASE_URL`.
- Renames: `WorkerConstants.productName` is "Doublangu worker", the menu's
  "Worker Settings…" is now "Settings…", and the web Settings section is
  "Workers" (route stays `/settings/workers`; protocol kind stays
  `speech-worker.v1`). On-disk paths keep `Doublangu Speech Worker` so existing
  installs keep their config, models, and logs.
- Tests: round-trip now asserts nil default URL; new acceptance/rejection tables
  for URL shapes, disk load without `base_url`, and `saveServerURL`
  persist/validate/clear behavior; relay AppState tests seed a server URL.

Note: the old URL remains in git history and in earlier DEVLOG entries; removing
it from history needs a rewrite (`git filter-repo`) plus force-push, which was
deliberately not done here.

Verification: `xcrun swift-format lint --recursive app/Sources app/Tests` clean;
`swift test --package-path app --parallel` exit 0 (93 tests, 0 failures);
`swift build --package-path app` exit 0; `npm --prefix web run check` 0 errors
0 warnings; `npx vitest run src/lib/routes/settings-layout.test.ts
src/lib/routes/speech-workers-page.test.ts` 9/9 pass; `git diff --check` clean;
`build-app.sh --development` rebuilt the v0.2.0 bundle as
`dist/macos/Doublangu worker.app` (CFBundleName/DisplayName renamed, binary
contains no server URL); `verify-release.sh --app-only` passed. The release
manifest records `application_commit: unknown` because the checkout had
untracked local model/audio files.
