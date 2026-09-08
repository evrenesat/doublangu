# internal/prompts

Owner-editable prompt version library: the five fixed instruction types,
their immutable stored versions, and the builtin defaults seeded as v1.

## Scope

- `types.go` — the closed PromptType set (linguistic_analysis,
  article_translation, explore, sentence_translation, correction), the
  immutable Version shape, CRLF→LF normalization, SHA-256 content hashing,
  and label/instruction bounds (80 scalars / 64 KiB).
- `defaults.go` — the builtin instructions. The four pre-existing texts are
  byte-exact copies of the annotator builders' instruction prefixes at
  baseline 7779c6d; their data envelopes, output schemas, and validation stay
  in the consuming packages. sentence_translation is the new operation's
  approved default.
- `store.go` — transactional, insert-only version storage: next-version
  allocation under one transaction, matching-type resolution, and the
  idempotent v1/default-selection seeding used at startup and on profile
  creation.

## Rules

- This package imports no annotator, reader, or analysis package.
- Stored versions are immutable: no code path updates or deletes a
  prompt_version row, and selections pin exact version ids (never "latest").
- `content_hash` is the plain SHA-256 over the exact normalized UTF-8 bytes.
- New instruction text is normalized only by CRLF→LF before save/hash;
  every other byte is preserved.
