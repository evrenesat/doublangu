# Doublangu

Doublangu is a self-hosted Dutch-to-English learning reader. A Go service owns
authentication, SQLite persistence, background enrichment, and media; a Svelte
5/SvelteKit app provides the library and reading experience.

## Current focus

The working product is an article reader for one owner:

- paste Dutch text and receive contextual English shadows from Codex
- choose the available Codex model and reasoning effort for new analysis runs
- inspect words, idioms, and discontinuous constructions without altering the
  source text
- mark individual meanings as learned
- leave and revisit articles while durable analysis jobs continue
- retry failed analysis, force a fresh run, and inspect retained owner-only run
  diagnostics
- play cached pronunciation and sentence narration when a compatible external
  macOS speech worker is connected

Reading and playback do not call an LLM. Analysis failures preserve the article
and can be retried. The speech worker is a separate companion project and is not
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

The API contract is in [`contracts/openapi.yaml`](contracts/openapi.yaml).
See [`ARCHITECTURE.md`](ARCHITECTURE.md) for system details and
[`DEVLOG.md`](DEVLOG.md) for implementation history.
