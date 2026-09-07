# Dictionary explore module

- This package owns the durable reader dictionary: subject resolution, the
  `dictionary_entry` record, and the `reader.dictionary.v1` server worker.
- Dependency direction: semantics, annotator, jobs, library, and store may be
  imported here; no reader/semantics/annotator package may import this one.
- Subject resolution is read-only and scoped to the article/reference join.
  It derives identity from stored sense lemma/canonical form, falling back to
  the exact source text; never from browser-supplied headwords.
- Dictionary generation never mutates semantic items, senses, article
  translations, construction membership, learning state, or analysis state.
  Generated alternate meanings are never passed to `EnsureSenseTx`.
- A ready entry is reused unchanged across articles, restarts, and provider
  changes; provenance is not an invalidation policy. Failures are terminal
  and need an explicit user retry (job `MaxAttempts` is 1).
- Publication is transactional: `PublishTx` requires `last_job_id` to still
  point at the publishing job and `jobs.CompleteTx` requires the live lease.
  A stale or canceled worker never publishes.
