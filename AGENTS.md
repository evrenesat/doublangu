# Doublangu Repository Guidelines

## Purpose and scope

Doublangu is a locally hosted Dutch-to-English learning reader. The Go service
owns authenticated APIs, persistence, enrichment orchestration, and provider
boundaries; the SvelteKit app owns the library and reader experience.

Follow the nearest `AGENTS.md` when working in a subdirectory. Preserve existing
user changes and work on the checked-out branch without committing unless the
owner explicitly requests a commit.

## Conventions

- Go code uses the repository's existing store, HTTP error, authentication,
  CSRF, ULID, and BCP-47 helpers. Keep domain logic testable and avoid import
  cycles. Run `gofmt` on changed Go files.
- Svelte code uses the existing dark product chrome, TypeScript settings, API
  client, and test conventions. Do not hand-edit generated API output.
- OpenAPI is the source for `web/src/lib/api/generated.ts`; regenerate it with
  `npm --prefix web run generate:api` and verify the generated diff is empty.
- Keep plan files read-only during implementation, except for the status line in
  `plans/article-hover-shadows-mvp.md` if that status must be updated.
- The owner explicitly authorized the isolated Mac reader-design environment
  described in README. Use `node tools/local-reader.mjs` for this UI iteration;
  do not attach production databases, provider configuration, or Mac workers.
  Keep the design branch synchronized with p100 at handoff, and preserve the
  `codex/p100-drift-preserved-20260905` / `backup/p100-drift-20260905` recovery refs.
  Do not run Svelte sync/build tasks concurrently with E2E tests in one checkout.

## Verification

Go 1.26.5 is installed at
`/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin`
but that directory may not be on the default shell `PATH`. Prefix Go, `gofmt`,
and Go-backed `make` commands with
`PATH=/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin:$PATH`.

From the repository root, run `make verify` (includes the `test-dictionary`
target: semantics/annotator/dictionary/jobs/store/httpapi/server-routing
tests plus the 013 migration rehearsal), the race tests for
`internal/dictionary`, `internal/jobs`, `internal/semantics`, and
`internal/httpapi`, `npm --prefix web run validate:openapi`, `generate:api`
(twice, byte-identical), `check`, the reader unit tests, `build`, the reader
E2E suites (`reader.spec.ts reader-design.spec.ts reader-progressive.spec.ts
reader-preference.spec.ts reader-explore.spec.ts`, run separately from
Svelte sync/build tasks), the opt-in authenticated Codex live tests, and
`git diff --check`. Record outcomes in `DEVLOG.md` and report the exact
commands at handoff.
