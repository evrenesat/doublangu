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
  and need an explicit user retry (job `MaxAttempts` is 1). An explicit
  regenerate always starts a fresh request — even on a ready or failed
  entry — while the saved document stays readable until a validated
  replacement publishes; retry and regenerate are mutually exclusive.
- Generation runs only the active profile's independent Explore binding plus
  that profile's pinned explore/correction prompt snapshots (captured,
  hash-verified, envelope-versioned); the translation binding of the same
  profile is never consulted.
- Every explore generation records section-5 history: the run is created
  before provider resolution with operation_type `explore` and the entry as
  subject, the entry's `last_run_id` points at it, and every completed or
  failed provider turn is retained through the executor's turn recorder.
- Publication is transactional: `PublishTx` requires `last_job_id` to still
  point at the publishing job and `jobs.CompleteTx` requires the live lease.
  A stale or canceled worker never publishes. The successful publication
  transaction also completes the history attempt and run, so a history
  storage failure rolls the publication back.
