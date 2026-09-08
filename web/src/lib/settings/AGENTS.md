# settings

Owner-facing analysis pipeline configuration UI.

## Scope

- `analysisProfiles.ts` — pure helpers for the provider/profile editor state:
  stage metadata, default options per provider type, option canonicalization
  for the wire request, completeness validation, the independent Explore
  binding draft, and the five fixed prompt-type selection helpers.
- `AnalysisPipelinePanel.svelte` — provider cards (live health + catalog +
  safe conformance test), profile CRUD, prompt-library loading, and
  active-profile selection. It owns the single profile draft/controller and
  decides when the editor opens.
- `ProfileEditor.svelte` — the profile form itself, rendered inline: inside
  the edited profile's list item (after its card), or below the New profile
  button for creation. Presentational only; save errors surface inside it,
  the name field is focused on open, focus returns to the opening trigger on
  close, and switching away from a dirty draft asks before discarding. It
  edits the two article stage bindings, the independent Explore binding, and
  pins one saved version for each of the five prompt types.
- `PromptLibraryPanel.svelte` — the saved prompt version library: type
  selector, version list, read-only instruction text, and Edit-as-new-version
  saves. Saving a version never activates it or changes any profile's pins.
- There is no legacy model/effort surface: `/api/v1/analysis/settings` is the
  active-profile contract and the panel is the only analysis editor;
  endpoints and secrets are never shown or edited in the browser.

## Conventions

- Never render `base_url`, endpoints, or secret values: the API returns
  neither, and the panel must not reintroduce them. Sanitize any provider
  error before display by reusing `providerTestErrorText`.
- Bindings are always built for both registered stages in registered order;
  the panel never saves a partial profile. The Explore binding is stored
  separately and never rewritten from a stage binding after creation.
- Prompt pins always reference exact saved version ids; there is no
  "latest" pin and saving a library version never repoints a profile.
- Tests live next to the code (`*.test.ts`); keep them pure by importing the
  helper module only.
