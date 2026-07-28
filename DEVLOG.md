# Devlog

## 2026-07-28 — Repository Bootstrap

### Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Go module path | `doublangu` | Self-hosted project, no external import needed. |
| Default port | `8080` | Common convention, avoids conflicts. |
| SvelteKit adapter | `@sveltejs/adapter-static` | Static output is simpler to deploy and sufficient for a single-user app. |
| Dev proxy pattern | Vite proxy `/api/*` and `/health/*` → Go `localhost:8080` | Avoids CORS during development, clean separation. |
| Go HTTP router | `net/http` stdlib `ServeMux` | No external dependency needed at this stage. |
| Version constant | `"0.1.0"` in `internal/httpapi` | Simple package-level const. |
| Pinned frontend dependencies | Exact versions (no caret/tilde ranges) | Reproducible builds; lockfile is authoritative. |
| Prettier | Local devDependency via `npm run format` | No network-fetched fallback. |
| PID-based process cleanup | `make dev` and `make smoke-health` trap + kill exact server PID | Avoids broad `pkill` that can terminate unrelated processes. |

### Package Structure

- **`cmd/doublangu-server`**: Main entry point — loads config, starts server,
  handles OS signals.
- **`internal/config`**: Typed config with env-var loading and validation.
- **`internal/httpapi`**: HTTP server with registered routes.
- **`web/`**: SvelteKit frontend with health-check landing page, static
  prerendering via `+layout.ts`.
