# Doublangu

Doublangu is a self-hosted Dutch-to-English learning reader. A Go service owns
authentication, SQLite persistence, background enrichment, and media; a Svelte
5/SvelteKit app provides the library and reading experience.

## Current focus

The working product is an article reader for one owner:

- paste Dutch text and receive contextual English subtitles from Codex
- choose the available Codex model and reasoning effort for new analysis runs
- inspect words, idioms, and discontinuous constructions without altering the
  source text
- mark individual meanings as learned
- leave and revisit articles while durable analysis jobs continue
- retry failed analysis, force a fresh run, and inspect retained owner-only run
  diagnostics
- play cached pronunciation and sentence narration when a compatible external
  macOS worker is connected

Reading and playback do not call an LLM. Analysis failures preserve the article
and can be retried. The worker is a separate companion project and is not
included here.

## Planned

The broader platform is intended to add URL and ebook ingestion, speech/text
alignment, spaced repetition, synchronized playback, offline use, mobile
packaging, additional language pairs, and alternate enrichment providers.
These are roadmap items, not current product features.

## Run locally

Requirements: Go 1.26.5, Node.js 24+, and an authenticated `codex` CLI.

```sh
npm --prefix web ci
codex login
export DOUBLANGU_SECRET="$(openssl rand -base64 32)"
go run ./cmd/doublangu-server --create-owner
```

Keep `DOUBLANGU_SECRET` unchanged, then start the API and web app in separate
terminals:

```sh
go run ./cmd/doublangu-server
npm --prefix web run dev
```

Open <http://localhost:5173/login>. Data defaults to `data/doublangu.db`; use
`DOUBLANGU_DB_PATH` to change it. Codex enrichment can be disabled with
`DOUBLANGU_ANNOTATOR=disabled`.

`DOUBLANGU_CODEX_MODEL` and `DOUBLANGU_CODEX_EFFORT` seed the initial analysis
selection only. After login, change the persisted selection from Settings;
later server restarts do not overwrite it.

For a deployment below a URL prefix, set the path when building the web app:

```sh
DOUBLANGU_WEB_BASE_PATH=/beta npm --prefix web run build
```

## Development

```sh
make verify
```

## Deployment

Every push to `main` is verified and packaged by GitHub Actions. A dedicated,
least-privilege self-hosted runner activates that exact artifact on the server.
The public workflow contains no deployment hostname, IP address, credentials,
or external health URL; the app's hostname and all runtime secrets remain in
protected server-side configuration. The server retains the existing owner
password and uses the built-in owner login as the public authentication layer.

The API contract is in [`contracts/openapi.yaml`](contracts/openapi.yaml).
See [`ARCHITECTURE.md`](ARCHITECTURE.md) for system details and
[`DEVLOG.md`](DEVLOG.md) for implementation history.

## Configurable analysis provider pipeline

The progressive reader analysis runs as a two-stage pipeline:
`linguistic_analysis` then `translation` per paragraph, each bound to a
configured provider in a profile.

- **Configuration file** (`DOUBLANGU_PROVIDER_CONFIG`): a strict, trusted JSON
  file owned by root or the service user, not group/world-writable, not
  world-readable, and not a symlink. See
  [`config/provider.example.json`](config/provider.example.json) for a
  service-owned template with placeholder model ids and an environment
  variable name for the secret — never put a credential or endpoint secret in
  the file or the browser. Secrets are referenced as `api_key_env` names that
  must match `^[A-Z_][A-Z0-9_]*$`.
- **Provider types**: `codex_app_server` (local Codex app server),
  `openai_compatible`, and `mac_relay`. OpenAI-compatible HTTPS base URLs may
  use `/v1` or `/api/v1`; HTTP base URLs are allowed only for literal loopback or
  the Tailscale CGNAT range when `allow_insecure_tailscale_http` is set. The
  server never returns, stores, or logs `base_url` or secrets; the owner API
  exposes only ids, labels, types, health, and model catalogs.
- **Compatibility mode**: without the config file the legacy
  `DOUBLANGU_ANNOTATOR` / `DOUBLANGU_CODEX_MODEL` / `DOUBLANGU_CODEX_EFFORT`
  single-provider path runs unchanged. The config file and legacy variables
  cannot be mixed.
- **Profiles and snapshots**: a profile binds each stage to a provider, model,
  and provider-specific options (codex `reasoning_effort`; omlx
  `temperature_milli` and `max_output_tokens`). Articles capture an immutable
  snapshot of the profile (id, name, bindings, and a domain-separated hash).
  New articles queue through the active profile; explicit fresh reanalysis may
  name another profile; normal retries reuse the stored snapshot — settings
  changes never mutate queued or running work, and a profile absent from a
  snapshot is never invoked.
- **Stage caches**: exact validated stage outputs are cached per paragraph on
  the identity `(stage, input hash, upstream artifact hash, contract, prompt,
  provider, model, options)`. Fresh runs bypass both caches; normal retries
  reuse only exact revalidated artifacts.
- **Failure and retry**: a failed article keeps its last accepted paragraphs
  readable; retry from the reader reuses the stored snapshot. Provider
  fingerprint changes fail closed (`v1.analysis_provider_changed`); offline or
  missing providers surface `v1.analysis_provider_unavailable`, and stage
  validation failures surface `v1.analysis_stage_failed` with the failed
  stage/provider recorded on the run.
- **Diagnostics and rollback**: run detail shows stage attempts with stable
  codes and truncated excerpts only; no prompts from failed pipeline stages
  are retained beyond per-turn artifacts. Rollback keeps migration 008
  additive: stop the server, remove the config file, and start again in
  compatibility mode against the same database.

## Progressive reader analysis (analysis contract v3)

New articles store deterministic source sentences at creation, queue narration
immediately, and analyze paragraph by paragraph: each validated paragraph is
published in its own transaction, so subtitles and progress appear while later
paragraphs are still running. Subtitle rendering participates in inline layout,
which prevents overlapping labels. Construction membership is exact
(`member_occurrence_ids`); inserted modifiers such as `bijna` keep their own
subtitles. Analysis progress (`analysis_progress`) and per-paragraph state
(`analysis_status`, `has_analysis`, `analysis_is_current`) are exposed on the
article API, and a compact progress surface reports queued/processing/failed
states with polite ARIA updates. Reanalysis keeps the last accepted semantics
visible in each paragraph until the replacement is validated and committed.
Pronounce-on-hover is an owner-wide preference (`GET`/`PUT
/api/v1/reader/settings`) that defaults to enabled; local storage only mirrors
the server value.
