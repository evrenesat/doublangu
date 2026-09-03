# settings

Owner-facing analysis pipeline configuration UI.

## Scope

- `analysisProfiles.ts` — pure helpers for the provider/profile editor state:
  stage metadata, default options per provider type, option canonicalization
  for the wire request, and completeness validation.
- `AnalysisPipelinePanel.svelte` — provider cards (live health + catalog +
  safe conformance test), profile CRUD, and active-profile selection.
- There is no legacy model/effort surface: `/api/v1/analysis/settings` is the
  active-profile contract and the panel is the only analysis editor;
  endpoints and secrets are never shown or edited in the browser.

## Conventions

- Never render `base_url`, endpoints, or secret values: the API returns
  neither, and the panel must not reintroduce them. Sanitize any provider
  error before display by reusing `providerTestErrorText`.
- Bindings are always built for both registered stages in registered order;
  the panel never saves a partial profile.
- Tests live next to the code (`*.test.ts`); keep them pure by importing the
  helper module only.
