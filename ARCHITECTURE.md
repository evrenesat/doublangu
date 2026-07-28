# Architecture — Skeleton

This document describes the current (skeleton) architecture of the Doublangu project.
It will grow as components are added.

## Overview

Doublangu is a self-hosted, single-user language-learning media system. At this
stage it consists of:

- A **Go HTTP server** in `cmd/doublangu-server/`
- A **SvelteKit static frontend** in `web/`

## Directory Layout

```
.
├── cmd/
│   └── doublangu-server/    # Main server entry point
├── internal/
│   ├── config/              # Typed configuration from env vars
│   └── httpapi/             # HTTP API server and handlers
├── web/                     # SvelteKit frontend
│   ├── src/
│   │   ├── routes/          # SvelteKit page routes
│   │   └── lib/             # Shared components
│   └── tests/               # Unit and E2E tests
├── Makefile                 # Dev workflow targets
├── ARCHITECTURE.md          # This file
└── DEVLOG.md                # Decision log
```

## Go Server

**Module:** `doublangu` (self-hosted, bare module path)

**Stack:** Go 1.26.5, stdlib `net/http` with `ServeMux` (no external router).

- `internal/config` — loads configuration from `DOUBLANGU_*` environment
  variables with sensible defaults (port 8080, 10s read/write timeouts, 60s
  idle timeout, 10s shutdown timeout).
- `internal/httpapi` — wraps `*http.Server`, registers routes. Currently only
  `GET /health/live` returns `{"status":"ok","version":"0.1.0"}`.

**Version constant:** `"0.1.0"` in `internal/httpapi`.

## Frontend

**Stack:** Svelte 5 / SvelteKit, TypeScript, Vitest, Playwright, Prettier.

**Adapter:** `@sveltejs/adapter-static` (pre-rendered static output).

**Prerendering:** Routes export `export const prerender = true` via `+layout.ts`.

**Dev proxy:** Vite dev server proxies `/api/*` and `/health/*` requests to Go
at `http://localhost:8080`, enabling seamless development without CORS.

**Dependencies:** Pinned to exact versions in `package.json` and regenerated
via `npm ci`.

## Development Workflow

| Command         | Description                       |
|-----------------|-----------------------------------|
| `make dev`      | Start Go server + web dev server  |
| `make test`     | Run all Go + web unit tests       |
| `make verify`   | Vet, type-check, unit tests, prod build |
| `make fmt`      | Format Go and web sources         |
| `make build`    | Build Go binary                   |
| `make smoke-health` | Quick smoke test of health endpoint |
