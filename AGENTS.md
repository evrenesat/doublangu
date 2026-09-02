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

## Verification

Go 1.26.5 is installed at
`/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin`
but that directory may not be on the default shell `PATH`. Prefix Go, `gofmt`,
and Go-backed `make` commands with
`PATH=/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.5.linux-amd64/bin:$PATH`.

From the repository root, run the focused Go tests and race tests, API
generation/checks, Svelte checks/unit and reader E2E tests, `make verify`, the
opt-in authenticated Codex live test, and `git diff --check` as listed in the
article-hover-shadows MVP handoff. Record outcomes in `DEVLOG.md` and report
the exact commands at handoff.
